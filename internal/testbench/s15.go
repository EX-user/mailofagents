package testbench

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// s15 — 唤醒并行度（boss report 0912「不并行/多个 session 在轮动」）：
// N 个账户在同一 tick 收到未读信，真 worker 的各账户 goroutine 接信唤醒
// 假 CLI；假 CLI 把 start/end 时间戳写进证据文件。三轴断言：
//   ① 隔离组重叠：独立 XDG_DATA_HOME 的两账户唤醒区间相交（并行）；
//   ② 共享组串行：共享同一 XDG_DATA_HOME 的两账户区间不相交（同
//      SQLite 库防 "database is locked" 的保守契约，钉死锁键语义防回退）；
//   ③ 隔离组起跑离散度 < 5s（同一拍齐发，无串行阶梯）。
// 起跑延迟分布入时间线（p50/p95 长期趋势指标）。

type s15wakeparallel struct{}

func (s15wakeparallel) Name() string { return "s15-wake-parallelism" }
func (s15wakeparallel) Desc() string {
	return "s15 唤醒并行度：隔离组并行/共享组串行/起跑离散度（多账户调度面）"
}
func (s15wakeparallel) Timeout() time.Duration { return 3 * time.Minute }

type s15span struct {
	tag        string
	start, end float64
}

func (s s15span) overlaps(o s15span) bool {
	return s.start <= o.end && o.start <= s.end
}

func (s s15wakeparallel) Run(ctx context.Context, env *Env) Result {
	res := Result{Scenario: s.Name(), OK: true, StartedAt: time.Now()}
	defer func() { res.Duration = time.Since(res.StartedAt) }()

	root := env.Root()
	srv, srvURL := s15OneShotServer()
	defer srv.Close()

	binDir := filepath.Join(env.RunDir, "fakebin")
	tl := filepath.Join(env.RunDir, "cli-timeline.log")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		res.add("fakebin", false, "%v", err)
		return res
	}
	fake := "#!/bin/bash\n" +
		"echo \"start $FAKE_TAG $(date +%s.%N)\" >> \"$S15_TIMELINE\"\n" +
		"echo '{\"type\":\"session\",\"id\":\"fake-'$FAKE_TAG'-$$\"}'\n" +
		"sleep 3\n" +
		"echo '{\"type\":\"message_update\",\"assistantMessageEvent\":{\"type\":\"text_delta\",\"delta\":\"'$FAKE_TAG' ev1\"}}'\n" +
		"sleep 3\n" +
		"echo \"end $FAKE_TAG $(date +%s.%N)\" >> \"$S15_TIMELINE\"\n" +
		"echo '{\"type\":\"message_update\",\"assistantMessageEvent\":{\"type\":\"text_end\",\"content\":\"'$FAKE_TAG' done\"}}'\n"
	if err := os.WriteFile(filepath.Join(binDir, "pi"), []byte(fake), 0o755); err != nil {
		res.add("fake_cli", false, "%v", err)
		return res
	}

	// 四账户：iso1/iso2 独立数据目录（并行组），shr1/shr2 共享（串行组）。
	agents := []struct {
		addr, tag, xdg string
	}{
		{"p1@fixture.test", "iso1", filepath.Join(env.RunDir, "xdg-1")},
		{"p2@fixture.test", "iso2", filepath.Join(env.RunDir, "xdg-2")},
		{"p3@fixture.test", "shr1", filepath.Join(env.RunDir, "xdg-shared")},
		{"p4@fixture.test", "shr2", filepath.Join(env.RunDir, "xdg-shared")},
	}
	var agentJSON []string
	for i, a := range agents {
		agentJSON = append(agentJSON, fmt.Sprintf(
			`{"address":%q,"password":"bench-fixture-pw","cli":"pi","workdir":%q,
			  "env":{"FAKE_TAG":%q,"S15_TIMELINE":%q,"XDG_DATA_HOME":%q}}`,
			a.addr, filepath.Join(env.RunDir, fmt.Sprintf("wd-%d", i+1)), a.tag, tl, a.xdg))
	}
	cfg := fmt.Sprintf(`{
  "server": %q, "poll_interval_sec": 1, "timeout_sec": 60,
  "agents": [%s]
}`, srvURL, strings.Join(agentJSON, ","))
	cfgPath := filepath.Join(env.RunDir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		res.add("config", false, "%v", err)
		return res
	}

	cmd := exec.Command(env.WorkerBin, "-config", cfgPath)
	cmd.Env = WhitelistEnv(root,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd.Dir = env.RunDir
	if err := cmd.Start(); err != nil {
		res.add("spawn", false, "%v", err)
		return res
	}
	// 观察窗：共享组串行 2×~7s ≈ 15s，隔离组并行 ~7s——45s 足够全部完成。
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			res.add("budget", false, "budget hit during observation")
			_ = cmd.Process.Kill()
			return res
		case <-time.After(500 * time.Millisecond):
		}
		if data, err := os.ReadFile(tl); err == nil && strings.Count(string(data), "end ") >= 4 {
			break
		}
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	_ = env.TL.Add("evidence", "s15 cli timeline", map[string]string{"path": tl})

	// 解析证据文件
	spans := map[string]s15span{}
	data, _ := os.ReadFile(tl)
	for _, ln := range strings.Split(string(data), "\n") {
		f := strings.Fields(ln)
		// line shape: "start <tag> <unix.ts>" / "end <tag> <unix.ts>"
		if len(f) != 3 || (f[0] != "start" && f[0] != "end") {
			continue
		}
		var ts float64
		if _, err := fmt.Sscanf(f[2], "%f", &ts); err != nil {
			continue
		}
		sp, ok := spans[f[1]]
		if !ok {
			sp = s15span{tag: f[1], start: ts, end: ts}
		}
		if ts < sp.start {
			sp.start = ts
		}
		if ts > sp.end {
			sp.end = ts
		}
		spans[f[1]] = sp
	}

	res.add("all_accounts_woke", len(spans) == len(agents),
		"want %d tagged CLI runs, got %d (%v)", len(agents), len(spans), keys(spans))
	if len(spans) != len(agents) {
		return res
	}

	iso1, iso2 := spans["iso1"], spans["iso2"]
	shr1, shr2 := spans["shr1"], spans["shr2"]

	// ① 隔离组并行
	res.add("isolated_pair_overlaps", iso1.overlaps(iso2),
		"isolated intervals must overlap: iso1[%.1f..%.1f] iso2[%.1f..%.1f]",
		iso1.start, iso1.end, iso2.start, iso2.end)

	// ② 共享组串行（防 SQLite 锁死的保守契约）
	res.add("shared_pair_serializes", !shr1.overlaps(shr2),
		"shared-store intervals must not overlap: shr1[%.1f..%.1f] shr2[%.1f..%.1f]",
		shr1.start, shr1.end, shr2.start, shr2.end)

	// ③ 隔离组起跑离散度
	disp := iso1.start - iso2.start
	if disp < 0 {
		disp = -disp
	}
	res.add("isolated_start_dispersion", disp < 5,
		"start dispersion %.2fs (budget 5s — same-poll wakes leave together)", disp)

	// 起跑延迟分布入时间线（长期趋势指标）
	var lags []float64
	minStart := minStartOf(spans)
	for _, sp := range spans {
		lags = append(lags, sp.start-minStart)
	}
	sort.Float64s(lags)
	p50 := lags[len(lags)/2]
	p95 := lags[(len(lags)*95)/100]
	_ = env.TL.Add("note", fmt.Sprintf("s15 start lag: p50=%.2fs p95=%.2fs max=%.2fs", p50, p95, lags[len(lags)-1]), nil)
	res.Notes = append(res.Notes, fmt.Sprintf("start-lag p50=%.2fs p95=%.2fs", p50, p95))
	return res
}

func minStartOf(spans map[string]s15span) float64 {
	min := -1.0
	for _, sp := range spans {
		if min < 0 || sp.start < min {
			min = sp.start
		}
	}
	return min
}

func keys(spans map[string]s15span) []string {
	var out []string
	for k := range spans {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func init() { register(s15wakeparallel{}) }

// s15OneShotServer serves one unread mail to every account; the mail goes
// read the moment /api/message is fetched — each account wakes EXACTLY
// once, which is what the parallelism spans assume (recurring wakes would
// smear the intervals across the whole window).
func s15OneShotServer() (*http.Server, string) {
	var mu sync.Mutex
	read := map[string]bool{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/inbox", func(w http.ResponseWriter, r *http.Request) {
		// serve unread ONCE per account, then read forever: the worker's
		// digest path never calls /api/message (the agent would), so the
		// read marker must flip here — otherwise the account re-wakes on
		// every poll and the parallelism spans smear across the window.
		who := basicUser(r)
		mu.Lock()
		unread := !read[who]
		read[who] = true
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"messages": []map[string]any{{
				"id": "01S15FIXTURE0000000000000X", "from": "actor@fixture.test",
				"subject": "stream a little", "preview": "stream",
				"unread": unread, "received_at": time.Now().Unix(), "files": 0,
			}},
			"unread_count": map[bool]int{true: 1, false: 0}[unread],
		})
	})
	mux.HandleFunc("/api/stats", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"inbox_total": 1, "unread": 1, "sent_total": 0})
	})
	mux.HandleFunc("/api/message", func(w http.ResponseWriter, r *http.Request) {
		who := basicUser(r)
		mu.Lock()
		read[who] = true
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message_id": "01S15FIXTURE0000000000000X", "from": "actor@fixture.test",
			"subject": "stream a little", "body": "stream", "attachments": []any{},
		})
	})
	mux.HandleFunc("/api/subs", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"subordinates": nil, "superiors": nil})
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return &http.Server{Addr: "127.0.0.1:0", Handler: http.NotFoundHandler()}, ""
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return srv, "http://" + ln.Addr().String()
}

func basicUser(r *http.Request) string {
	u, _, ok := r.BasicAuth()
	if !ok || u == "" {
		return "anon"
	}
	return u
}
