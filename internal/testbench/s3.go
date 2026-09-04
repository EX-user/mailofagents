package testbench

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// S3 — 看门狗掐断自愈：真 worker 带一条「慢 CLI」（BENCH_SLOW=5s）与
// timeout_sec=2 起跑：每轮唤醒都被看门狗 SIGINT 掐断（进程组级），
// 断言四点：
//   ① 掐断如实入账：日志现「timeout interrupt after 2s (session kept,
//      mail re-queued)」；
//   ② 自愈：worker 不退不僵，后续 poll 照常再唤醒（信重排队）；
//   ③ 会话保持：重投轮的唤醒带 -s（resume 原会话，上下文不丢）；
//   ④ 不计连败：超时是预期自愈路径，不得触发值守告警链。
//
// 硬门禁全程活扫（长活进程）。

type s3watchdog struct{}

func (s3watchdog) Name() string { return "s3-watchdog-selfheal" }
func (s3watchdog) Desc() string {
	return "S3 看门狗掐断自愈：timeout interrupt 入账/自愈再醒/会话保持/不计连败"
}
func (s3watchdog) Timeout() time.Duration { return 3 * time.Minute }

func (s s3watchdog) Run(ctx context.Context, env *Env) Result {
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

	srv, srvURL := newFixtureMailServer([]MailSummary{{
		ID: "01FIXTURE0000000000000000X", From: "actor@fixture.test",
		Subject: fixtureSubject, Preview: fixturePreview,
		Unread: true, ReceivedAt: time.Now().Unix(),
	}})
	defer srv.Close()

	binDir := filepath.Join(env.RunDir, "fakebin")
	evDir := filepath.Join(env.RunDir, "evidence")
	if err := writeFakeCLIsMulti(binDir, evDir); err != nil {
		res.add("fake_cli", false, "%v", err)
		return res
	}
	evFile := filepath.Join(evDir, "s3-calls.txt")

	cfgPath := filepath.Join(env.RunDir, "config.json")
	cfg := fmt.Sprintf(`{
  "server": %q, "poll_interval_sec": 1, "timeout_sec": 2,
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
		"BENCH_SLOW=5", // 慢 CLI：每轮唤醒必被 2s 看门狗掐断
	)
	cmd.Dir = env.RunDir
	logBuf := &strings.Builder{}
	cmd.Stdout, cmd.Stderr = logBuf, logBuf

	_ = env.TL.Add("note", "s3 start (slow CLI 5s vs watchdog 2s)", nil)
	if err := cmd.Start(); err != nil {
		res.add("spawn", false, "%v", err)
		return res
	}

	// 硬门禁（长活进程）
	ok, detail := scanLive(cmd.Process.Pid, 3*time.Second)
	res.add("environ_gate", ok, "%s", detail)

	// 候两轮调用：第一轮被掐 → 重投 → 第二轮 resume。
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(evFile); err == nil && strings.Count(string(b), "=== CALL ===") >= 2 {
			break
		}
		select {
		case <-ctx.Done():
			res.add("budget", false, "budget hit waiting for re-wake evidence")
			_ = cmd.Process.Kill()
			return res
		case <-time.After(200 * time.Millisecond):
		}
	}
	time.Sleep(300 * time.Millisecond)
	alive := cmd.Process.Signal(syscall.Signal(0)) == nil
	res.add("worker_survives", alive, "worker still running after repeated watchdog kills")
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()

	log := logBuf.String()
	_ = env.TL.Add("evidence", "s3 worker log", map[string]int{"bytes": len(log)})

	// ① 掐断如实入账
	res.add("timeout_interrupt_logged", strings.Contains(log, "timeout interrupt after 2s"),
		"log carries the timeout-interrupt line (session kept, mail re-queued)")

	// ③ 会话保持：证据文件里 resume 调用出现在被掐调用之后
	ev, err := os.ReadFile(evFile)
	if err != nil {
		res.add("evidence_read", false, "%v", err)
		return res
	}
	blocks := strings.Split(string(ev), "=== CALL ===")
	blocks = blocks[1:] // 首段是空头
	if len(blocks) < 2 {
		res.add("rewake_happened", false, "want >=2 invocations, got %d", len(blocks))
		return res
	}
	res.add("rewake_happened", true, "%d invocations recorded", len(blocks))
	first, second := blocks[0], blocks[1]
	res.add("first_wake_fresh", !strings.Contains(first, "-- -s"), "first (killed) wake was fresh")
	res.add("second_wake_resumes", strings.Contains(second, "-- -s") && strings.Contains(second, "-- "+sess),
		"post-kill re-queued wake resumes %q", sess)

	// ④ 不计连败：无值守告警链（超时路径明确不进 failStreak）
	res.add("no_failure_alert", !strings.Contains(log, "值守连败告警") && !strings.Contains(log, "alert sent"),
		"timeout path stays out of the failure streak")
	return res
}

func init() { register(s3watchdog{}) }
