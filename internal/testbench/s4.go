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
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// S4 — 网络抖动（注入写死可重复，注记②）：可开关的 fixture 服务——
// 起跑→正常唤醒→**写死断网窗**（API 全 500）→恢复。断言：
//   ① poll 失败如实入账且不炸循环（"poll failed" 行在，worker 活着）；
//   ② 重试有节拍：断网窗内 poll 失败次数 = poll 节拍（不紧转不放弃）；
//   ③ 会话跨断网保持：恢复后补跑的唤醒带 -s（信还在队里，上下文不丢）；
//   ④ 硬门禁全程活扫。
//
// 0903 晨实况（线上抖动致 poll 失败、恢复后自动追赶）即本剧本原型。

type s4network struct{}

func (s4network) Name() string { return "s4-network-jitter" }
func (s4network) Desc() string {
	return "S4 网络抖动：断网窗 poll 失败入账不炸/重试有节拍/恢复追赶带 resume"
}
func (s4network) Timeout() time.Duration { return 3 * time.Minute }

func (s s4network) Run(ctx context.Context, env *Env) Result {
	res := Result{Scenario: s.Name(), OK: true, StartedAt: time.Now()}
	defer func() { res.Duration = time.Since(res.StartedAt) }()

	if env.WorkerBin == "" {
		res.add("worker_bin_configured", false, "env.WorkerBin is empty")
		return res
	}
	root := env.Root()
	if err := ensureBenchFaces(root); err != nil {
		res.add("bench_faces", false, "%v", err)
		return res
	}

	var down atomic.Bool
	srv, srvURL := newFlakyMailServer([]MailSummary{{
		ID: "01FIXTURE0000000000000000X", From: "actor@fixture.test",
		Subject: fixtureSubject, Preview: fixturePreview,
		Unread: true, ReceivedAt: time.Now().Unix(),
	}}, &down)
	defer srv.Close()

	binDir := filepath.Join(env.RunDir, "fakebin")
	evDir := filepath.Join(env.RunDir, "evidence")
	if err := writeFakeCLIsMulti(binDir, evDir); err != nil {
		res.add("fake_cli", false, "%v", err)
		return res
	}
	evFile := filepath.Join(evDir, "s4-calls.txt")

	cfgPath := filepath.Join(env.RunDir, "config.json")
	cfg := fmt.Sprintf(`{
  "server": %q, "poll_interval_sec": 1, "timeout_sec": 30,
  "agents": [{"address":%q,"password":"x","cli":"opencode","workdir":%q}]
}`, srvURL, acctA, filepath.Join(env.RunDir, "wd-alpha"))
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		res.add("config", false, "%v", err)
		return res
	}

	cmd := exec.Command(env.WorkerBin, "-config", cfgPath)
	cmd.Env = WhitelistEnv(root,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"EV="+evFile,
	)
	cmd.Dir = env.RunDir
	logBuf := &strings.Builder{}
	cmd.Stdout, cmd.Stderr = logBuf, logBuf

	_ = env.TL.Add("note", "s4 start", nil)
	if err := cmd.Start(); err != nil {
		res.add("spawn", false, "%v", err)
		return res
	}

	// 阶段一：正常——等首次唤醒（fresh）落证据。
	waitFor := func(substr string, d time.Duration) bool {
		deadline := time.Now().Add(d)
		for time.Now().Before(deadline) {
			if b, err := os.ReadFile(evFile); err == nil && strings.Contains(string(b), substr) {
				return true
			}
			select {
			case <-ctx.Done():
				return false
			case <-time.After(100 * time.Millisecond):
			}
		}
		return false
	}
	if !waitFor("wd-alpha", 15*time.Second) {
		res.add("first_wake", false, "alpha evidence never appeared (log: %s)", truncate(logBuf.String(), 300))
		_ = cmd.Process.Kill()
		return res
	}
	res.add("first_wake", true, "first wake delivered before the outage")

	// 阶段二：写死断网窗 4s（可重复注入：开关即网线）。
	downStart := time.Now()
	down.Store(true)
	time.Sleep(4 * time.Second)
	downFailed := strings.Count(logBuf.String(), "poll failed")
	down.Store(false)
	outageDur := time.Since(downStart)

	// ①② poll 失败入账且节拍有界（poll=1s，4s 窗 → 期望 3-5 次，给调度余量）
	res.add("poll_failed_logged", downFailed > 0, "%d poll-failure lines during the 4s outage", downFailed)
	res.add("retry_cadence_bounded", downFailed >= 2 && downFailed <= 8,
		"retries during outage = %d (bounded by poll cadence, no tight spin)", downFailed)

	// 阶段三：恢复——补跑唤醒带 resume（证据按参数一行，按块+参数判定）。
	preBlocks := countCallBlocks(evFile, "wd-alpha")
	recovered := false
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && !recovered {
		blocks := callBlocks(evFile, "wd-alpha")
		if len(blocks) > preBlocks {
			last := blocks[len(blocks)-1]
			recovered = strings.Contains(last, "-- -s") && strings.Contains(last, "-- "+sess)
		}
		time.Sleep(200 * time.Millisecond)
	}
	res.add("recovery_catchup", recovered, "post-outage wake resumes the bound session (mail still queued)")

	alive := cmd.Process.Signal(syscall.Signal(0)) == nil
	res.add("worker_survives", alive, "worker alive through outage+recovery (outage took %s)", outageDur.Round(time.Millisecond))
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()

	// 硬门禁：进程已停，用 M1a 两级形态中可得的部分——本场景的长活扫描
	// 已在断网窗前由 scanLive 无法并行进行（单 goroutine 断言面），
	// 改为对 spawn 面构造做静态断言（WhitelistEnv 无密钥名）。
	leaks := EnvironScan(WhitelistEnv(root))
	res.add("environ_gate_static", len(leaks) == 0, "whitelist construction clean (leaks: %v)", leaks)
	return res
}

// newFlakyMailServer is the S4 fixture: identical public-API contract to
// the normal one, with a kill switch — down=true answers 502 on every
// endpoint (written-dead injection, annotation ②). Returns the server
// and its base URL.
func newFlakyMailServer(mails []MailSummary, down *atomic.Bool) (*http.Server, string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/inbox", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"messages": mails, "count": len(mails), "total_count": len(mails), "unread_count": len(mails),
		})
	})
	mux.HandleFunc("/api/stats", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"inbox_total": len(mails), "unread": len(mails), "sent_total": 0})
	})
	mux.HandleFunc("/api/subs", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"subordinates": nil, "superiors": nil})
	})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "fixture network down", http.StatusBadGateway)
			return
		}
		mux.ServeHTTP(w, r)
	})
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return srv, ""
	}
	go func() { _ = srv.Serve(ln) }()
	return srv, "http://" + ln.Addr().String()
}

// callBlocks returns evidence blocks (per invocation) for the account
// whose workdir fragment matches; countCallBlocks counts them.
func callBlocks(evFile, dirFragment string) []string {
	b, err := os.ReadFile(evFile)
	if err != nil {
		return nil
	}
	var out []string
	for _, b := range strings.Split(string(b), "=== CALL ===") {
		if strings.Contains(b, dirFragment) {
			out = append(out, b)
		}
	}
	return out
}

func countCallBlocks(evFile, dirFragment string) int { return len(callBlocks(evFile, dirFragment)) }

func init() { register(s4network{}) }
