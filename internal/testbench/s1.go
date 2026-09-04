package testbench

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// S1 — config 演进：config1（单账户）起跑一阵 → 改 config 新增账户 →
// 重启 → 断言白皮书四点：
//   ① 新账户被接管（acctB 的假 CLI 被调用，全新会话无 resume）；
//   ② 旧会话不断（acctA 重启后带 -s <id> 续谈）；
//   ③ 状态文件不串（每账户独立 state 文件，各自绑定各自的 session）；
//   ④ 单进程多账户（run2 两账户的调用同一 $PPID）。
//
// 附：硬门禁全窗形态——两代 worker 进程都是长活进程，/proc environ
// 活体扫描全程可用（比 M1a 的探针兜底更强的证据形态）。

type s1evolution struct{}

func (s1evolution) Name() string { return "s1-config-evolution" }
func (s1evolution) Desc() string {
	return "S1 config 演进：新增账户接管/旧会话续谈/state 不串/单进程多账户 + 长活进程硬门禁"
}
func (s1evolution) Timeout() time.Duration { return 3 * time.Minute }

const (
	acctA = "alpha@fixture.test"
	acctB = "bravo@fixture.test"
	sess  = "s1fake"
)

func (s s1evolution) Run(ctx context.Context, env *Env) Result {
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
	evFile := filepath.Join(evDir, "s1-calls.txt")

	writeConfig := func(names ...string) error {
		agents := ""
		for i, n := range names {
			if i > 0 {
				agents += ","
			}
			agents += fmt.Sprintf(`{"address":%q,"password":"x","cli":"opencode","workdir":%q}`,
				n, filepath.Join(env.RunDir, "wd-"+strings.SplitN(n, "@", 2)[0]))
		}
		cfg := fmt.Sprintf(`{"server":%q,"poll_interval_sec":1,"timeout_sec":30,"agents":[%s]}`,
			srvURL, agents)
		return os.WriteFile(filepath.Join(env.RunDir, "config.json"), []byte(cfg), 0o644)
	}

	spawn := func() (*exec.Cmd, *bytes.Buffer, error) {
		cmd := exec.Command(env.WorkerBin, "-config", filepath.Join(env.RunDir, "config.json"))
		cmd.Env = WhitelistEnv(root, "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"), "EV="+evFile)
		cmd.Dir = env.RunDir
		buf := &bytes.Buffer{}
		cmd.Stdout, cmd.Stderr = buf, buf
		return cmd, buf, cmd.Start()
	}
	waitEvidence := func(ctx context.Context, substr string, d time.Duration) bool {
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
	stop := func(cmd *exec.Cmd) {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		time.Sleep(200 * time.Millisecond)
	}

	// ---- run1：config1 = [alpha] ----
	if err := writeConfig(acctA); err != nil {
		res.add("config1", false, "%v", err)
		return res
	}
	cmd1, log1, err := spawn()
	if err != nil {
		res.add("spawn1", false, "%v", err)
		return res
	}
	_ = env.TL.Add("note", "s1 run1 start", map[string]any{"pid": cmd1.Process.Pid, "agents": 1})

	// 硬门禁（长活进程全窗）：run1 进程活体扫描。
	ok1, detail1 := scanLive(cmd1.Process.Pid, 3*time.Second)
	res.add("environ_gate_run1", ok1, "%s", detail1)
	if !ok1 {
		stop(cmd1)
		return res
	}

	if !waitEvidence(ctx, "wd-alpha", 15*time.Second) {
		res.add("alpha_wake_run1", false, "alpha evidence never appeared (log: %s)", truncate(log1.String(), 300))
		stop(cmd1)
		return res
	}
	res.add("alpha_wake_run1", true, "alpha fake CLI invoked")
	time.Sleep(1500 * time.Millisecond) // 让本轮 wake 完成、state 落盘后再停
	stop(cmd1)

	// ---- run2：config2 = [alpha, bravo] ----
	if err := writeConfig(acctA, acctB); err != nil {
		res.add("config2", false, "%v", err)
		return res
	}
	cmd2, log2, err := spawn()
	if err != nil {
		res.add("spawn2", false, "%v", err)
		return res
	}
	_ = env.TL.Add("note", "s1 run2 start", map[string]any{"pid": cmd2.Process.Pid, "agents": 2})
	defer stop(cmd2)

	if !waitEvidence(ctx, "wd-bravo", 15*time.Second) {
		res.add("bravo_takeover", false, "bravo evidence never appeared (log: %s)", truncate(log2.String(), 300))
		return res
	}
	time.Sleep(2 * time.Second) // 让 alpha 的第二次调用也落盘

	ev, err := os.ReadFile(evFile)
	if err != nil {
		res.add("evidence_read", false, "%v", err)
		return res
	}
	evidence := string(ev)
	_ = env.TL.Add("evidence", "s1 full call log", map[string]int{"bytes": len(evidence)})

	// ① 新账户接管：bravo 有调用
	res.add("bravo_takeover", strings.Contains(evidence, "wd-bravo"), "bravo fake CLI invoked after config grew")

	// ② 旧会话不断：alpha 最后一次调用带 -s <session>
	alphaBlocks := blocksWith(evidence, "wd-alpha")
	res.add("alpha_blocks", len(alphaBlocks) >= 2, "alpha invoked %d times across restart (want >=2)", len(alphaBlocks))
	if len(alphaBlocks) >= 2 {
		last := alphaBlocks[len(alphaBlocks)-1]
		// 证据里 argv 逐参数一行（"-- -s" / "-- s1fake"），按参数出现判定。
		resume := strings.Contains(last, "-- -s") && strings.Contains(last, "-- "+sess)
		res.add("alpha_resume", resume, "alpha's post-restart wake resumes %q", sess)
		first := alphaBlocks[0]
		fresh := !strings.Contains(first, "-- -s") && !strings.Contains(first, "-- "+sess)
		res.add("alpha_fresh_before_restart", fresh, "alpha's pre-restart wake was a fresh session")
	}

	// ③ 状态文件不串：两个独立 state 文件，各自绑定
	stA, errA := os.ReadFile(filepath.Join(env.RunDir, "config.alpha.state.json"))
	stB, errB := os.ReadFile(filepath.Join(env.RunDir, "config.bravo.state.json"))
	res.add("state_files_distinct", errA == nil && errB == nil,
		"alpha state err=%v, bravo state err=%v", errA, errB)
	if errA == nil && errB == nil {
		res.add("state_bindings_bound", strings.Contains(string(stA), sess) && strings.Contains(string(stB), sess),
			"both state files bound to %q, in separate files", sess)
	}

	// ④ 单进程多账户：run2 内 alpha/bravo 的调用同一 $PPID
	ppidsA, ppidsB := ppidsIn(blocksContaining(evidence, "wd-alpha"), 2), ppidsIn(blocksContaining(evidence, "wd-bravo"), 2)
	same := false
	for _, a := range ppidsA {
		for _, b := range ppidsB {
			if a == b {
				same = true
			}
		}
	}
	res.add("single_process_multi_account", same, "run2 parent pids: alpha=%v bravo=%v", ppidsA, ppidsB)

	// 硬门禁 run2（长活进程）
	ok2, detail2 := scanLive(cmd2.Process.Pid, 3*time.Second)
	res.add("environ_gate_run2", ok2, "%s", detail2)
	return res
}

// scanLive polls /proc/<pid>/environ while the process runs — the
// long-lived form of the M1a hard gate.
func scanLive(pid int, window time.Duration) (bool, string) {
	deadline := time.Now().Add(window)
	scans := 0
	for time.Now().Before(deadline) {
		if environ, err := ReadProcEnviron(pid); err == nil {
			scans++
			if leaks := EnvironScan(environ); len(leaks) > 0 {
				return false, fmt.Sprintf("secret-named vars leaked: %v", leaks)
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	if scans == 0 {
		return false, "UNVERIFIABLE: /proc never readable in window"
	}
	return true, fmt.Sprintf("%d live scans clean (patterns: %d)", scans, len(secretPatterns))
}

// blocksWith splits the evidence log into per-invocation blocks that
// mention dirFragment (the workdir identifies the account).
func blocksWith(evidence, dirFragment string) []string {
	var out []string
	for _, b := range strings.Split(evidence, "=== CALL ===") {
		if strings.Contains(b, dirFragment) {
			out = append(out, b)
		}
	}
	return out
}

// ppidsIn extracts distinct PPID values from evidence blocks.
func ppidsIn(blocks []string, limit int) []string {
	seen := map[string]bool{}
	var out []string
	for _, b := range blocks {
		for _, line := range strings.Split(b, "\n") {
			if p, ok := strings.CutPrefix(line, "PPID="); ok && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

func blocksContaining(evidence, frag string) []string { return blocksWith(evidence, frag) }

// writeFakeCLIsMulti is the S1 fake CLI: appends (never truncates), tags
// every invocation with a CALL separator and its parent pid, dumps argv
// and stdin, and answers parseable session JSON.
func writeFakeCLIsMulti(binDir, evDir string) error {
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(evDir, 0o755); err != nil {
		return err
	}
	script := `#!/bin/bash
{
  echo "=== CALL ==="
  echo "PPID=$PPID"
  echo "=== ARGV ==="
  for a in "$@"; do echo "-- $a"; done
  echo "=== STDIN ==="
  cat
} >> "$EV"
# 真 CLI 启动即宣告会话（worker 的 salvage/续谈链依赖这一点），随后才是长活工作。
printf '{"type":"thread.started","thread_id":"%s"}\n{"sessionID":"%s"}\n'
sleep "${BENCH_SLOW:-0}"
`
	p := filepath.Join(binDir, "opencode")
	if err := os.WriteFile(p, []byte(fmt.Sprintf(script, sess, sess)), 0o755); err != nil {
		return err
	}
	// codex not needed for S1, but keep the dir symmetric for future runs.
	return nil
}

func init() { register(s1evolution{}) }
