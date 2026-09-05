package testbench

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// s9 — time_beat 定时打断 e2e（boss spec 2026-09-05）：
//
// 配置 time_beat 槽位（动态=now+35s 取整分，保证 5-65s 后到点），慢 CLI
// （BENCH_SLOW=70s）保证首个唤醒必跨槽位 → watchBeat 打断（内部 5min
// 最小间隔首次必放行）→ 立即重唤醒且 digest 带 [报时] 行。断言：
//   ① 打断入账：log 带 "time-beat interrupt"；
//   ② [报时] 行进摘要（证据文件可见）；
//   ③ worker 全程存活。
//
// 空箱也醒的语义顺带被验证：fixture 信箱虽有一封信，但打断发生在信已
// 消费后的长唤醒中——[报时] 强制唤醒路径与信件无关。

type s9timebeat struct{}

func (s9timebeat) Name() string { return "s9-time-beat-interrupt" }
func (s9timebeat) Desc() string {
	return "s9 time_beat 定时打断：槽位触发打断/重唤醒带[报时]/存活"
}
func (s9timebeat) Timeout() time.Duration { return 4 * time.Minute }

func (s s9timebeat) Run(ctx context.Context, env *Env) Result {
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

	// 槽位：now+35s 取整分，若距现在不足 5s 则顺延一分钟（保证 5-65s）。
	slot := time.Now().Add(35 * time.Second).Truncate(time.Minute)
	for !slot.After(time.Now().Add(5 * time.Second)) {
		slot = slot.Add(time.Minute)
	}
	sched := fmt.Sprintf("{%02d:%02d}", slot.Hour(), slot.Minute())

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
	evFile := filepath.Join(evDir, "s9-calls.txt")

	cfg := fmt.Sprintf(`{
  "server": %q, "poll_interval_sec": 1, "timeout_sec": 120,
  "time_beat": %q,
  "agents": [{"address":%q,"password":"x","cli":"opencode","workdir":%q}]
}`, srvURL, sched, acctA, filepath.Join(env.RunDir, "wd-alpha"))
	cfgPath := filepath.Join(env.RunDir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		res.add("config", false, "%v", err)
		return res
	}

	cmd := exec.Command(env.WorkerBin, "-config", cfgPath)
	cmd.Env = WhitelistEnv(root,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"EV="+evFile,
		"BENCH_SLOW=70", // 长唤醒必跨槽位
	)
	cmd.Dir = env.RunDir
	logBuf := &bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = logBuf, logBuf

	_ = env.TL.Add("note", "s9 start (slot="+sched+" at "+slot.Format("15:04:05")+", slow CLI 70s)", nil)
	if err := cmd.Start(); err != nil {
		res.add("spawn", false, "%v", err)
		return res
	}

	// 候打断+重唤醒：log 出现打断行，证据出现 [报时]。
	interruptSeen, beatSeen := false, false
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) && !(interruptSeen && beatSeen) {
		if b, err := os.ReadFile(evFile); err == nil {
			beatSeen = strings.Contains(string(b), "[报时]")
		}
		interruptSeen = strings.Contains(logBuf.String(), "time-beat interrupt")
		select {
		case <-ctx.Done():
		case <-time.After(200 * time.Millisecond):
		}
	}
	alive := cmd.Process.Signal(syscall.Signal(0)) == nil
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	log := logBuf.String()
	logPath := filepath.Join(env.RunDir, "s9-worker.log")
	_ = os.WriteFile(logPath, []byte(log), 0o644)
	_ = env.TL.Add("evidence", "s9 worker log", map[string]any{"bytes": len(log), "path": logPath})

	res.add("beat_interrupt_logged", interruptSeen, "log carries the time-beat interrupt line")
	res.add("beat_line_in_digest", beatSeen, "re-wake digest carries the [报时] line (evidence file)")
	res.add("worker_survives", alive, "worker alive through beat interrupt+re-wake")
	return res
}

func init() { register(s9timebeat{}) }
