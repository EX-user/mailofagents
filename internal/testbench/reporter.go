package testbench

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Reporter (M3 final piece). Three jobs, all mechanical and replayable —
// no judgment calls, every score cites the facts that produced it:
//
//  1. Three-axis rubric per scenario (whitepaper axes):
//     链质量 chain   — was the machinery sound (no panic/timeout, timeline
//     actually recorded the run, artifacts present)?
//     时序纪律 timing — budget discipline (duration vs the scenario's
//     Timeout) and timeline monotonicity.
//     语义正确 semantic — assertion pass ratio (behavior itself).
//     A failing assertion never dents chain quality: the chain is fine,
//     the behavior wasn't. That separation is the point of three axes.
//
//  2. Replay pack: replay/report.md (human-readable account citing
//     evidence) + replay/manifest.json (sha256 of every file in the run
//     directory) — enough to review or re-run a batch offline.
//
//  3. Retention: runs are pruned after 30 days (annotation ④); FAILED
//     runs are archived under <runs>/failed/ and exempt from pruning so
//     failure trends survive the window. Any failing batch mails ONE
//     digest to the main test line (cc the architect) via the public
//     mail API — credentials never enter the letter body.
//
// Retention window and failure recipients are whitepaper constants; tune
// them in review, not per-invocation.

const (
	// RetentionWindow is how long normal (passed) run directories stay
	// on disk before pruning (annotation ④: 30-day window).
	RetentionWindow = 30 * 24 * time.Hour
	// FailedRunsDir holds archived failed runs, exempt from pruning.
	FailedRunsDir = "failed"
)

// AxisScore is one rubric axis: 0–100 plus the facts that set the score.
type AxisScore struct {
	Axis  string   `json:"axis"`
	Score int      `json:"score"`
	Facts []string `json:"facts"`
}

// BatchScore is the scored view of one bench batch.
type BatchScore struct {
	PerScenario map[string][]AxisScore `json:"per_scenario"`
	// AxisAverages are the mean scores across scenarios (order: chain,
	// timing, semantic — same as axisNames).
	AxisAverages []int `json:"axis_averages"`
	// AxisMins surface the worst scenario per axis (a healthy average
	// over one rotten scenario is exactly what staging must see).
	AxisMins []int `json:"axis_mins"`
}

var axisNames = []string{"chain", "timing", "semantic"}

// ScoreResult scores one scenario result along the three axes. budget is
// the scenario's Timeout (0 → the runner default); tlPath is the run's
// timeline (scored for presence and monotonicity).
func ScoreResult(res Result, budget time.Duration, tlPath string) []AxisScore {
	if budget <= 0 {
		budget = defaultScenarioTimeout
	}
	events := readTimelineEvents(tlPath)

	// --- 链质量 chain: machinery soundness ---
	chain := AxisScore{Axis: axisNames[0], Score: 100}
	for _, a := range res.Assertions {
		if a.Name == "panic" && !a.OK {
			chain.Score -= 50
			chain.Facts = append(chain.Facts, "panic recorded in assertions")
		}
	}
	if len(events) == 0 {
		chain.Score -= 30
		chain.Facts = append(chain.Facts, "timeline recorded nothing for this run")
	}
	if chain.Score < 100 && len(chain.Facts) == 0 {
		chain.Facts = append(chain.Facts, "chain intact")
	}
	if chain.Score == 100 {
		chain.Facts = append(chain.Facts,
			fmt.Sprintf("no panic; timeline carries %d events", len(events)))
	}

	// --- 时序纪律 timing: budget discipline + evidence ordering ---
	timing := AxisScore{Axis: axisNames[1], Score: 100}
	ratio := res.Duration.Seconds() / budget.Seconds()
	switch {
	case res.Duration > budget:
		timing.Score -= 60
		timing.Facts = append(timing.Facts, fmt.Sprintf("duration %s exceeded budget %s", res.Duration.Round(time.Millisecond), budget))
	case ratio > 0.8:
		timing.Score -= 20
		timing.Facts = append(timing.Facts, fmt.Sprintf("duration %s is %.0f%% of budget %s", res.Duration.Round(time.Millisecond), ratio*100, budget))
	default:
		timing.Facts = append(timing.Facts, fmt.Sprintf("duration %s of budget %s (%.0f%%)", res.Duration.Round(time.Millisecond), budget, ratio*100))
	}
	if nonMonotonic(events) {
		timing.Score -= 30
		timing.Facts = append(timing.Facts, "timeline timestamps are not monotonic")
	}

	// --- 语义正确 semantic: assertion pass ratio ---
	sem := AxisScore{Axis: axisNames[2], Score: 0}
	if len(res.Assertions) > 0 {
		passed := 0
		for _, a := range res.Assertions {
			if a.OK {
				passed++
			}
		}
		sem.Score = passed * 100 / len(res.Assertions)
		sem.Facts = append(sem.Facts, fmt.Sprintf("%d/%d assertions passed", passed, len(res.Assertions)))
	} else {
		sem.Facts = append(sem.Facts, "no assertions evaluated")
	}
	for _, a := range res.Assertions {
		if !a.OK {
			sem.Facts = append(sem.Facts, "✗ "+a.Name+": "+truncate(a.Detail, 160))
		}
	}
	return []AxisScore{chain, timing, sem}
}

// ScoreBatch scores every result and rolls up per-axis averages and mins.
// runDir is the batch root: each scenario's timeline lives at
// <runDir>/<sanitized scenario>/timeline.jsonl.
func ScoreBatch(runDir string, results []Result) *BatchScore {
	b := &BatchScore{PerScenario: map[string][]AxisScore{}}
	sums := []int{0, 0, 0}
	mins := []int{100, 100, 100}
	counted := 0
	for _, res := range results {
		budget := time.Duration(0)
		if sc, ok := Lookup(res.Scenario); ok {
			budget = sc.Timeout()
		}
		tlPath := filepath.Join(runDir, sanitize(res.Scenario), "timeline.jsonl")
		axes := ScoreResult(res, budget, tlPath)
		b.PerScenario[res.Scenario] = axes
		counted++
		for i, ax := range axes {
			sums[i] += ax.Score
			if ax.Score < mins[i] {
				mins[i] = ax.Score
			}
		}
	}
	if counted > 0 {
		for i := range sums {
			b.AxisAverages = append(b.AxisAverages, sums[i]/counted)
			b.AxisMins = append(b.AxisMins, mins[i])
		}
	}
	return b
}

// readTimelineEvents loads a timeline JSONL (capped); missing files are
// simply an empty event list — the chain axis treats that as a fact.
func readTimelineEvents(path string) []Event {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []Event
	dec := json.NewDecoder(f)
	for i := 0; i < 20000; i++ {
		var e Event
		if dec.Decode(&e) != nil {
			break
		}
		out = append(out, e)
	}
	return out
}

func nonMonotonic(events []Event) bool {
	for i := 1; i < len(events); i++ {
		if events[i].At.Before(events[i-1].At) {
			return true
		}
	}
	return false
}

// WriteReport persists report.json: raw results plus the scored view —
// the machine-readable half of the replay pack (the human half is
// BuildReplay's report.md).
func WriteReport(runDir string, results []Result, batch *BatchScore) error {
	type report struct {
		GeneratedAt time.Time   `json:"generated_at"`
		Results     []Result    `json:"results"`
		Score       *BatchScore `json:"score,omitempty"`
	}
	body, err := json.MarshalIndent(report{GeneratedAt: time.Now(), Results: results, Score: batch}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(runDir, "report.json"), body, 0o644)
}

// BuildReplay writes the replay pack into <runDir>/replay/: report.md
// (the human account) and manifest.json (sha256 over every other file in
// the run directory). It returns the report path.
func BuildReplay(runDir string, results []Result, batch *BatchScore) (string, error) {
	repDir := filepath.Join(runDir, "replay")
	if err := os.MkdirAll(repDir, 0o755); err != nil {
		return "", err
	}
	report := renderReport(runDir, results, batch)
	reportPath := filepath.Join(repDir, "report.md")
	if err := os.WriteFile(reportPath, []byte(report), 0o644); err != nil {
		return "", err
	}
	manifest, err := buildManifest(runDir, repDir)
	if err != nil {
		return "", err
	}
	mb, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	return reportPath, os.WriteFile(filepath.Join(repDir, "manifest.json"), mb, 0o644)
}

func renderReport(runDir string, results []Result, batch *BatchScore) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 台架批次回放报告 / bench replay report\n\n- 生成: %s\n- 运行目录: `%s`\n- 场景: %d\n\n",
		time.Now().Format("2006-01-02 15:04:05 -0700"), runDir, len(results))
	if batch != nil && len(batch.AxisAverages) == 3 {
		fmt.Fprintf(&b, "## 三轴总览（chain=链质量 timing=时序纪律 semantic=语义正确）\n\n")
		fmt.Fprintf(&b, "| 轴 | 均值 | 最低 |\n|---|---|---|\n")
		for i, ax := range axisNames {
			fmt.Fprintf(&b, "| %s | %d | %d |\n", ax, batch.AxisAverages[i], batch.AxisMins[i])
		}
		b.WriteString("\n")
	}
	sorted := make([]Result, len(results))
	copy(sorted, results)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Scenario < sorted[j].Scenario })
	for _, res := range sorted {
		mark := "PASS"
		if !res.OK {
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "## %s — %s（%d 断言，%s）\n\n", res.Scenario, mark, len(res.Assertions), res.Duration.Round(time.Millisecond))
		if axes := batch.PerScenario[res.Scenario]; len(axes) == 3 {
			fmt.Fprintf(&b, "- 链质量 %d：%s\n", axes[0].Score, strings.Join(axes[0].Facts, "；"))
			fmt.Fprintf(&b, "- 时序纪律 %d：%s\n", axes[1].Score, strings.Join(axes[1].Facts, "；"))
			fmt.Fprintf(&b, "- 语义正确 %d：%s\n", axes[2].Score, strings.Join(axes[2].Facts, "；"))
		}
		b.WriteString("\n| 断言 | 结果 | 证据 |\n|---|---|---|\n")
		for _, a := range res.Assertions {
			mark := "✓"
			if !a.OK {
				mark = "✗"
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s |\n", a.Name, mark, strings.ReplaceAll(a.Detail, "|", "\\|"))
		}
		tlRel := filepath.Join(res.Scenario, "timeline.jsonl")
		fmt.Fprintf(&b, "\n时间线: `%s`；重放: `go run ./cmd/worker-testbench -scenarios %s -worker-bin <bin>`\n\n", tlRel, res.Scenario)
	}
	return b.String()
}

// manifestEntry is one replay-pack file record.
type manifestEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func buildManifest(runDir, excludeDir string) ([]manifestEntry, error) {
	var out []manifestEntry
	err := filepath.Walk(runDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if strings.HasPrefix(path, excludeDir+string(os.PathSeparator)) || path == excludeDir {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		rel, err := filepath.Rel(runDir, path)
		if err != nil {
			return err
		}
		out = append(out, manifestEntry{Path: rel, Size: info.Size(), SHA256: hex.EncodeToString(sum[:])})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

// PruneRuns deletes run directories under runsRoot older than keep
// (annotation ④: 30-day window). The failed/ archive is exempt. Names
// that do not parse as run timestamps are left alone.
func PruneRuns(runsRoot string, keep time.Duration, now time.Time) ([]string, error) {
	entries, err := os.ReadDir(runsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var pruned []string
	for _, e := range entries {
		if !e.IsDir() || e.Name() == FailedRunsDir {
			continue
		}
		ts, err := time.Parse("20060102-150405", e.Name())
		if err != nil {
			continue
		}
		if now.Sub(ts) > keep {
			if err := os.RemoveAll(filepath.Join(runsRoot, e.Name())); err != nil {
				return pruned, err
			}
			pruned = append(pruned, e.Name())
		}
	}
	return pruned, nil
}

// ArchiveFailed moves a failed run directory under <runsRoot>/failed/
// (failure reports outlive the 30-day window — annotation ④ 另存).
func ArchiveFailed(runsRoot, runDir string) (string, error) {
	dstRoot := filepath.Join(runsRoot, FailedRunsDir)
	if err := os.MkdirAll(dstRoot, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dstRoot, filepath.Base(runDir))
	if _, err := os.Stat(dst); err == nil {
		dst = fmt.Sprintf("%s-%d", dst, time.Now().UnixNano()%1_000_000)
	}
	return dst, os.Rename(runDir, dst)
}

// MailFailure sends ONE digest letter for a failing batch (whitepaper:
// failures mail out to the main test line, cc the architect). The body
// cites failing assertions and artifact paths — never credentials.
func MailFailure(server, addr, pass string, to, cc []string, results []Result, runDir string) error {
	var b strings.Builder
	failed := 0
	sort.Slice(results, func(i, j int) bool { return results[i].Scenario < results[j].Scenario })
	for _, res := range results {
		if res.OK {
			continue
		}
		failed++
		fmt.Fprintf(&b, "## %s（%d 断言，%s）\n", res.Scenario, len(res.Assertions), res.Duration.Round(time.Millisecond))
		for _, a := range res.Assertions {
			if !a.OK {
				fmt.Fprintf(&b, "- ✗ %s: %s\n", a.Name, truncate(a.Detail, 200))
			}
		}
		fmt.Fprintf(&b, "- 时间线: `%s`\n\n", filepath.Join(runDir, res.Scenario, "timeline.jsonl"))
	}
	if failed == 0 {
		return nil
	}
	body := fmt.Sprintf("台架批次失败：%d/%d 个场景未过。\n\n运行目录: `%s`\n回放包: `%s`\n\n%s\n（机械摘录，凭据不入信——凭据只在台架 config。）\n\n—— worker-testbench reporter",
		failed, len(results), runDir, filepath.Join(runDir, "replay"), b.String())
	payload := map[string]any{"to": to, "cc": cc,
		"subject": fmt.Sprintf("【台架 FAIL】%d/%d 场景未过 @%s", failed, len(results), time.Now().Format("0102-15:04:05")),
		"body":    body, "public": true}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(server, "/")+"/api/send", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.SetBasicAuth(addr, pass) // 全址 Basic（短址会 401）
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("mail failure report: status %d", resp.StatusCode)
	}
	return nil
}
