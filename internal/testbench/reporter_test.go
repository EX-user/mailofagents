package testbench

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTestTimeline(t *testing.T, path string, events ...Event) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScoreResultAxes(t *testing.T) {
	dir := t.TempDir()
	tlPath := filepath.Join(dir, "s0", "timeline.jsonl")
	base := time.Now()
	writeTestTimeline(t, tlPath,
		Event{At: base, Kind: "note", What: "scenario start"},
		Event{At: base.Add(time.Second), Kind: "evidence", What: "artifact"},
	)

	// Healthy pass: full marks on all three axes, facts cite evidence.
	res := Result{Scenario: "s0", OK: true, Duration: 2 * time.Second,
		Assertions: []Assertion{{Name: "a", OK: true, Detail: "ok"}}}
	axes := ScoreResult(res, time.Minute, tlPath)
	if axes[0].Score != 100 || axes[1].Score != 100 || axes[2].Score != 100 {
		t.Fatalf("healthy run scored %v", axes)
	}
	if !strings.Contains(axes[0].Facts[0], "2 events") {
		t.Fatalf("chain facts should cite timeline size: %v", axes[0].Facts)
	}

	// Panic dents chain, never semantic-by-itself.
	panicky := Result{Scenario: "s0", Duration: time.Second,
		Assertions: []Assertion{{Name: "panic", OK: false, Detail: "scenario panicked"}}}
	axes = ScoreResult(panicky, time.Minute, tlPath)
	if axes[0].Score != 50 {
		t.Fatalf("panic should cost 50 chain: %d", axes[0].Score)
	}
	if axes[2].Score != 0 {
		t.Fatalf("failing assertion should zero semantic here: %d", axes[2].Score)
	}

	// Empty timeline dents chain.
	axes = ScoreResult(res, time.Minute, filepath.Join(dir, "none", "timeline.jsonl"))
	if axes[0].Score != 70 || !strings.Contains(axes[0].Facts[0], "nothing") {
		t.Fatalf("empty timeline should cost 30 chain: %+v", axes[0])
	}

	// Budget discipline: 85% of budget −20, over budget −60.
	near := res
	near.Duration = 52 * time.Second
	if got := ScoreResult(near, time.Minute, tlPath)[1].Score; got != 80 {
		t.Fatalf("near-budget timing should be 80, got %d", got)
	}
	over := res
	over.Duration = 61 * time.Second
	if got := ScoreResult(over, time.Minute, tlPath)[1].Score; got != 40 {
		t.Fatalf("over-budget timing should be 40, got %d", got)
	}

	// Non-monotonic timeline dents timing.
	writeTestTimeline(t, filepath.Join(dir, "s9", "timeline.jsonl"),
		Event{At: base.Add(5 * time.Second), Kind: "note"},
		Event{At: base, Kind: "note"},
	)
	if got := ScoreResult(res, time.Minute, filepath.Join(dir, "s9", "timeline.jsonl"))[1].Score; got != 70 {
		t.Fatalf("non-monotonic timing should be 70, got %d", got)
	}

	// Semantic is the assertion ratio, with per-failure citations.
	half := Result{Scenario: "s0",
		Assertions: []Assertion{
			{Name: "good", OK: true},
			{Name: "bad", OK: false, Detail: "want x"},
		}}
	sem := ScoreResult(half, time.Minute, tlPath)[2]
	if sem.Score != 50 || len(sem.Facts) != 2 {
		t.Fatalf("half pass should score 50 with a citation: %+v", sem)
	}
}

func TestScoreBatchRollup(t *testing.T) {
	dir := t.TempDir()
	// ScoreBatch reads timelines relative to the batch root.
	writeTestTimeline(t, filepath.Join(dir, sanitize("selfcheck"), "timeline.jsonl"),
		Event{At: time.Now(), Kind: "note"})
	writeTestTimeline(t, filepath.Join(dir, sanitize("unknown-scenario"), "timeline.jsonl"),
		Event{At: time.Now(), Kind: "note"})
	results := []Result{
		{Scenario: "selfcheck", OK: true, Duration: time.Second,
			Assertions: []Assertion{{Name: "a", OK: true}}},
		{Scenario: "unknown-scenario", OK: true, Duration: time.Second,
			Assertions: []Assertion{{Name: "a", OK: true}}},
	}
	b := ScoreBatch(dir, results)
	if len(b.AxisAverages) != 3 || len(b.AxisMins) != 3 {
		t.Fatalf("rollup missing: %+v", b)
	}
	for i, ax := range axisNames {
		if b.AxisAverages[i] != 100 || b.AxisMins[i] != 100 {
			t.Fatalf("axis %s: want 100/100, got %v/%v", ax, b.AxisAverages[i], b.AxisMins[i])
		}
	}
}

func TestBuildReplayPack(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "report.json"), []byte(`{"results":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTestTimeline(t, filepath.Join(dir, sanitize("selfcheck"), "timeline.jsonl"), Event{At: time.Now(), Kind: "note"})
	results := []Result{{Scenario: "selfcheck", OK: true, Duration: time.Second,
		Assertions: []Assertion{{Name: "info_reachable", OK: true, Detail: "domain=example"}}}}
	batch := ScoreBatch(dir, results)
	reportPath, err := BuildReplay(dir, results, batch)
	if err != nil {
		t.Fatal(err)
	}
	report, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"selfcheck", "info_reachable", "chain", "回放报告"} {
		if !strings.Contains(string(report), want) {
			t.Fatalf("report.md missing %q:\n%s", want, report)
		}
	}
	// Manifest covers every file outside replay/ with a verifiable hash.
	manRaw, err := os.ReadFile(filepath.Join(dir, "replay", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var man []manifestEntry
	if err := json.Unmarshal(manRaw, &man); err != nil {
		t.Fatal(err)
	}
	if len(man) != 2 { // report.json + timeline.jsonl; replay/ itself excluded
		t.Fatalf("manifest entries = %d, want 2: %+v", len(man), man)
	}
	for _, e := range man {
		if len(e.SHA256) != 64 {
			t.Fatalf("bad hash for %s: %q", e.Path, e.SHA256)
		}
	}
}

func TestPruneRunsAndArchive(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	mk := func(name string) string {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	oldRun := mk("20250101-000000") // 30-day window: pruned
	recent := mk(now.Format("20060102-150405"))
	mk("not-a-run")                    // ignored
	mk(filepath.Join("failed", "x01")) // exempt subtree

	pruned, err := PruneRuns(root, RetentionWindow, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(pruned) != 1 || pruned[0] != "20250101-000000" {
		t.Fatalf("pruned = %v", pruned)
	}
	if _, err := os.Stat(oldRun); !os.IsNotExist(err) {
		t.Fatalf("old run should be gone, err=%v", err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatalf("recent run should stay: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "not-a-run")); err != nil {
		t.Fatal("non-run dir should stay")
	}
	if _, err := os.Stat(filepath.Join(root, "failed", "x01")); err != nil {
		t.Fatal("failed archive is exempt from pruning")
	}

	// ArchiveFailed moves the run under failed/ and never clobbers.
	src := mk(now.Format("20060102-150405"))
	dst, err := ArchiveFailed(root, src)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(filepath.Dir(dst)) != FailedRunsDir {
		t.Fatalf("archive target %q not under failed/", dst)
	}
	mk(filepath.Base(src)) // recreate, then re-archive → suffixed, not clobbered
	dst2, err := ArchiveFailed(root, src)
	if err != nil {
		t.Fatal(err)
	}
	if dst2 == dst {
		t.Fatal("second archive should not clobber the first")
	}
}

func TestMailFailure(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		fmt.Fprint(w, `{"message_id":"01X","status":"sent"}`)
	}))
	defer srv.Close()

	addr, pass := "actor@fixture.test", "super-secret-password"
	results := []Result{{Scenario: "s0", Duration: time.Second,
		Assertions: []Assertion{
			{Name: "urgent_delivered", OK: false, Detail: "digest lacks the urgent mail"},
		}}}

	err := MailFailure(srv.URL, addr, pass,
		[]string{"moa-dev-engineer@mailofagents.online"},
		[]string{"moa-arch-engineer@mailofagents.online"},
		results, "/runs/20260905-010000")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || gotPath != "/api/send" {
		t.Fatalf("want one /api/send call, got %d to %s", calls, gotPath)
	}
	// Basic auth must carry the FULL address (short address 401s).
	if gotAuth == "" || strings.Contains(gotAuth, "actor@:") == false && !strings.Contains(gotAuth, "YWN0b3JAZml4dHVyZS50ZXN0") {
		t.Fatalf("basic auth should encode the full address: %q", gotAuth)
	}
	body, _ := gotBody["body"].(string)
	for _, want := range []string{"urgent_delivered", "/runs/20260905-010000", "s0", "凭据不入信"} {
		if !strings.Contains(body, want) {
			t.Fatalf("failure body missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, pass) {
		t.Fatal("failure body must never carry credentials")
	}
	if tos, _ := gotBody["to"].([]any); len(tos) != 1 || tos[0] != "moa-dev-engineer@mailofagents.online" {
		t.Fatalf("wrong recipients: %v", gotBody["to"])
	}

	// Healthy batch: no mail at all.
	calls = 0
	if err := MailFailure(srv.URL, addr, pass, []string{"x@y"}, nil,
		[]Result{{Scenario: "s0", OK: true}}, "/runs/x"); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("passing batch must not mail")
	}
}
