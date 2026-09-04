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

// s6 — 压缩提醒确定性触发链（注记①：极小 notice_tokens 让触发确定）。
// codex 线（无 worker 可调压缩入口——「会话保留+内建压缩兜底」形态）：
//   wake1：假 CLI 上报超大 usage（input_tokens=123456 ≥ 阈值 1000）
//          → 日志「notice round next」；
//   wake2：digest 带压缩预告（persist-memory 指令）——确定性触发；
//   wake3：预告消失（一轮即止），会话 id 全程未轮换。
//
// 断言的是 worker 侧编排语义（触发/预告/清除/不轮换），非 CLI 压缩本身。

type s6notice struct{}

func (s6notice) Name() string { return "s6-compact-notice" }
func (s6notice) Desc() string {
	return "s6 压缩提醒确定性触发链：超大 usage→预告轮→清除，会话不轮换"
}
func (s6notice) Timeout() time.Duration { return 3 * time.Minute }

const noticeMarker = "[压缩预告 / Session compaction notice]"

func (s s6notice) Run(ctx context.Context, env *Env) Result {
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
	evFile := filepath.Join(evDir, "s6-calls.txt")

	// codex 线 + 极小阈值（1000）+ 假 CLI 固定上报 123456 → 必然触发。
	cfgPath := filepath.Join(env.RunDir, "config.json")
	cfg := fmt.Sprintf(`{
  "server": %q, "poll_interval_sec": 1, "timeout_sec": 30,
  "compact_notice_tokens": 1000,
  "agents": [{"address":%q,"password":"x","cli":"codex","workdir":%q}]
}`, srvURL, acctA, filepath.Join(env.RunDir, "wd-alpha"))
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		res.add("config", false, "%v", err)
		return res
	}

	// codex 版假 CLI：宣告会话 → 上报超大 usage → 正常退出。
	codexScript := fmt.Sprintf(`#!/bin/bash
{
  echo "=== CALL ==="
  echo "PPID=$PPID"
  echo "=== ARGV ==="
  for a in "$@"; do echo "-- $a"; done
  echo "=== STDIN ==="
  cat
} >> "$EV"
printf '{"type":"thread.started","thread_id":"%%s"}\n{"sessionID":"%%s"}\n{"type":"item.completed","usage":{"input_tokens":123456}}\n' "%s" "%s"
`, sess, sess)
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(codexScript), 0o755); err != nil {
		res.add("fake_codex", false, "%v", err)
		return res
	}

	cmd := exec.Command(env.WorkerBin, "-config", cfgPath)
	cmd.Env = WhitelistEnv(root,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"EV="+evFile,
	)
	cmd.Dir = env.RunDir
	logBuf := &bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = logBuf, logBuf

	_ = env.TL.Add("note", "s6 start (notice_tokens=1000, usage=123456)", nil)
	if err := cmd.Start(); err != nil {
		res.add("spawn", false, "%v", err)
		return res
	}

	// 候三轮调用：w1（触发）→ w2（预告）→ w3（预告清除）。
	// 注：codex 适配器不带 --dir（workdir 走 cmd.Dir），故按全块计数。
	waitCalls := func(n int, d time.Duration) bool {
		deadline := time.Now().Add(d)
		for time.Now().Before(deadline) {
			if countAllBlocks(evFile) >= n {
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
	if !waitCalls(3, 30*time.Second) {
		res.add("three_wakes", false, "3 wakes never completed (log: %s)", truncate(logBuf.String(), 300))
		_ = cmd.Process.Kill()
		return res
	}
	res.add("three_wakes", true, "3 invocations recorded")
	alive := cmd.Process.Signal(syscall.Signal(0)) == nil
	res.add("worker_survives", alive, "worker alive through the notice chain")
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()

	log := logBuf.String()
	res.add("trigger_logged", strings.Contains(log, "notice round next"),
		"log carries \"context tokens 123456 >= notice threshold 1000\"")

	blocks := callAllBlocks(evFile)
	w2 := blocks[len(blocks)-2]
	w3 := blocks[len(blocks)-1]
	res.add("notice_in_wake2", strings.Contains(w2, noticeMarker), "wake2 digest carries the compaction notice")
	res.add("notice_cleared_wake3", !strings.Contains(w3, noticeMarker), "wake3 digest is notice-free (one round only)")

	// 会话不轮换：三轮全部 resume 同一会话（w1 fresh 之后）。
	resumeCount := 0
	for i, b := range blocks {
		if i > 0 && strings.Contains(b, "-- "+sess) {
			resumeCount++
		}
	}
	res.add("session_never_rotated", resumeCount == len(blocks)-1,
		"%d/%d post-first wakes resumed the same session", resumeCount, len(blocks)-1)
	return res
}

// callAllBlocks returns every invocation block in the evidence file.
func callAllBlocks(evFile string) []string {
	b, err := os.ReadFile(evFile)
	if err != nil {
		return nil
	}
	var out []string
	for _, b := range strings.Split(string(b), "=== CALL ===") {
		if strings.TrimSpace(b) != "" {
			out = append(out, b)
		}
	}
	return out
}

func countAllBlocks(evFile string) int { return len(callAllBlocks(evFile)) }

func init() { register(s6notice{}) }
