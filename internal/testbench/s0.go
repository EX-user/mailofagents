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
	"time"
)

// S0 — 真实事故复现（端到端第二道门）：
//   - opencode argv 丢失案：digest 走 argv，曾有字段丢失/截断；
//   - codex stdin 截断案：digest 走 stdin，多行/CJK 曾被截。
//
// 本场景让真 worker 用【假 CLI】跑真实 wake 管道：mock 邮件服务投递
// 构造信（CJK/多行/二进制工作流措辞），假 CLI 把收到的 argv/stdin 原样
// 落盘为证据，台架断言「CLI 实际收到的」与「worker 构造的」零损耗。
// 不配真 key、不触真 LLM——CLI 是我们的脚本（boss 指令 3 的标准形态）。

type s0construction struct{}

func (s0construction) Name() string { return "s0-construction" }
func (s0construction) Desc() string {
	return "S0 真实事故复现：opencode argv / codex stdin 通道 digest 端到端零损耗"
}
func (s0construction) Timeout() time.Duration { return 3 * time.Minute }

// fixtureSubject/Preview 复刻原事故内容族：二进制格式工作流 + CJK + 长词。
const (
	fixtureSubject = "PE/MZ 判定与 series 交付确认"
	fixturePreview = "EINVAL 复现参数附后：分片补发清单已就绪，15 段 arch-slice-0001.bin…arch-slice-0015.bin 全部落树位 ready，等值守唤醒认领处理。"
)

func (s s0construction) Run(ctx context.Context, env *Env) Result {
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

	// --- fixture mail server: the worker's public-API surface ---
	mails := []MailSummary{{
		ID: "01FIXTURE0000000000000000X", From: "actor@fixture.test",
		Subject: fixtureSubject, Preview: fixturePreview,
		Unread: true, ReceivedAt: time.Now().Unix(),
	}}
	srv, srvURL := newFixtureMailServer(mails)
	defer srv.Close()

	// --- fake CLI: records argv + stdin as evidence, answers parseable JSON ---
	binDir := filepath.Join(env.RunDir, "fakebin")
	evDir := filepath.Join(env.RunDir, "evidence")
	if err := writeFakeCLIs(binDir, evDir); err != nil {
		res.add("fake_cli", false, "%v", err)
		return res
	}

	workdir := filepath.Join(env.RunDir, "workdir")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		res.add("workdir", false, "%v", err)
		return res
	}

	// opencode 通道：digest 走 argv（原丢失案通道）
	res2 := s.runOne(ctx, env, res, channelRun{
		label: "opencode-argv", cli: "opencode", server: srvURL, evFile: "opencode-argv.txt",
		root: root, binDir: binDir, workdir: workdir,
		want: []string{fixtureSubject, "分片补发清单", "EINVAL"},
	})
	res.Assertions = append(res.Assertions, res2.Assertions...)
	if !res2.OK {
		res.OK = false
	}

	// codex 通道：digest 走 stdin（原截断案通道）
	res3 := s.runOne(ctx, env, res, channelRun{
		label: "codex-stdin", cli: "codex", server: srvURL, evFile: "codex-stdin.txt",
		root: root, binDir: binDir, workdir: workdir,
		want: []string{fixtureSubject, "分片补发清单", "EINVAL"},
	})
	res.Assertions = append(res.Assertions, res3.Assertions...)
	if !res3.OK {
		res.OK = false
	}
	return res
}

type channelRun struct {
	label, cli, server, evFile, root, binDir, workdir string
	want                                              []string
}

func (s s0construction) runOne(ctx context.Context, env *Env, res Result, cr channelRun) Result {
	out := Result{Scenario: cr.label, OK: true}

	cfgPath := filepath.Join(env.RunDir, "worker-"+cr.label+".json")
	cfg := fmt.Sprintf(`{
  "server": %q, "address": "watched@fixture.test", "password": "x",
  "cli": %q, "workdir": %q,
  "poll_interval_sec": 1, "timeout_sec": 30
}`, cr.server, cr.cli, cr.workdir)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		out.add("config", false, "%v", err)
		return out
	}

	pathEnv := os.Getenv("PATH")
	cmd := exec.Command(env.WorkerBin, "-config", cfgPath)
	cmd.Env = WhitelistEnv(cr.root,
		"PATH="+cr.binDir+string(os.PathListSeparator)+pathEnv,
		"EV="+filepath.Join(env.RunDir, "evidence", cr.evFile),
		"TESTBENCH_MOCK=1",
	)
	cmd.Dir = cr.workdir
	var wout strings.Builder
	cmd.Stdout, cmd.Stderr = &wout, &wout

	_ = env.TL.Add("note", "worker spawned (s0 "+cr.label+")", map[string]any{"bin": env.WorkerBin, "cli": cr.cli})
	if err := cmd.Start(); err != nil {
		out.add("spawn_"+cr.label, false, "%v", err)
		return out
	}

	// 候证据：假 CLI 被调用的那一刻，digest 已在通道里。
	evPath := filepath.Join(env.RunDir, "evidence", cr.evFile)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(evPath); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			out.add("budget", false, "scenario budget hit waiting for evidence")
			_ = cmd.Process.Kill()
			return out
		case <-time.After(100 * time.Millisecond):
		}
	}
	time.Sleep(300 * time.Millisecond) // 让 CLI 写完证据
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()

	ev, err := os.ReadFile(evPath)
	if err != nil {
		out.add("evidence_"+cr.label, false, "fake CLI was never invoked within 30s (worker log: %s)", truncate(wout.String(), 300))
		return out
	}
	got := string(ev)
	_ = env.TL.Add("evidence", "channel payload "+cr.label, map[string]any{"bytes": len(got)})
	out.add("channel_delivered_"+cr.label, len(got) > 0, "%d bytes reached the CLI", len(got))
	for _, w := range cr.want {
		out.add("intact_"+cr.label+"_"+sanitize(w), strings.Contains(got, w), "want %q in channel payload", w)
	}
	if strings.Contains(got, "SAMPLE DIGEST") {
		out.add("not_sample_"+cr.label, false, "evidence carries the -plan placeholder — pipeline bypassed")
	}
	// 预览截断语义：超 60 rune 的预览应以省略号收尾（worker 端 clamp）。
	out.add("preview_clamped_"+cr.label, strings.Contains(got, "…"), "long preview carries the clamp ellipsis")
	return out
}

// newFixtureMailServer serves the worker's public-API surface: inbox,
// stats, subs. Auth headers are not checked — the worker's credential
// handling is not under test here.
func newFixtureMailServer(mails []MailSummary) (*http.Server, string) {
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
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return &http.Server{Addr: "127.0.0.1:0", Handler: http.NotFoundHandler()}, ""
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return srv, "http://" + ln.Addr().String()
}

// writeFakeCLIs installs record-everything stand-ins for opencode/codex.
// Both answer with parseable session/thread JSON so the worker's wake
// completes cleanly (no alert noise).
func writeFakeCLIs(binDir, evDir string) error {
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(evDir, 0o755); err != nil {
		return err
	}
	const (
		argvScript = `#!/bin/bash
{ printf '=== ARGV (%d args) ===\n'; for a in "$@"; do printf '%s\n' "-- $a"; done; } > "$EV"
printf '{"type":"thread.started","thread_id":"s0fake"}\n{"session_id":"s0fake"}\n'
`
		stdinScript = `#!/bin/bash
{ printf '=== ARGV (%d args) ===\n'; for a in "$@"; do printf '%s\n' "-- $a"; done; printf '=== STDIN ===\n'; cat; } > "$EV"
printf '{"type":"thread.started","thread_id":"s0fake"}\n{"session_id":"s0fake"}\n'
`
	)
	for name, script := range map[string]string{
		"opencode": argvScript,
		"codex":    stdinScript,
	} {
		p := filepath.Join(binDir, name)
		if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
			return err
		}
	}
	return nil
}

func init() { register(s0construction{}) }
