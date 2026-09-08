package testbench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// s12 — claude 真成功路径（opt-in: TESTBENCH_REAL=1）：真 claude code ×
// 真 deepseek 有余额 key，走 anthropic 兼容端点。仿 s10：邮件面 fixture，
// 真模型真处理一封邮件，成功静默——证据在盘面。
//
// 接线（REALRUN_RESEARCH.md §3，alice 裁准 2026-09-07）：key 通道首选
// ~/.claude/settings.json 的 env 块（claude 原生文件通道，key 不进
// worker environ——/proc 扫描面口径不变）。端点=deepseek anthropic 兼容
// （https://api.deepseek.com/anthropic），模型名写死 deepseek-chat（该端
// 点命名；一次冒烟即过，2026-09-07）。
//
// 断言（仿 s10/s11）：①state 绑定 session ②零错误归档 ③零 wake failed
// ④存活。claudeAdapter 解析顶层 session_id 与实测输出一致。

type s12claudereal struct{}

func (s12claudereal) Name() string { return "s12-claude-real-success" }
func (s12claudereal) Desc() string {
	return "s12 claude 真成功路径（opt-in TESTBENCH_REAL=1）：真 claude×有余额 key（settings.json 通道），会话绑定+零错误"
}
func (s12claudereal) Timeout() time.Duration { return 8 * time.Minute }

func (s s12claudereal) Run(ctx context.Context, env *Env) Result {
	res := Result{Scenario: s.Name(), OK: true, StartedAt: time.Now()}
	defer func() { res.Duration = time.Since(res.StartedAt) }()

	if os.Getenv("TESTBENCH_REAL") != "1" {
		res.add("opt_in_skip", true, "TESTBENCH_REAL != 1 — real-key scenario skipped")
		return res
	}
	if env.WorkerBin == "" {
		res.add("worker_bin_configured", false, "env.WorkerBin is empty")
		return res
	}
	root := env.Root()
	if err := ensureBenchFaces(root); err != nil {
		res.add("bench_faces", false, "%v", err)
		return res
	}

	cfgRaw, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		res.add("bench_config", false, "read bench config: %v", err)
		return res
	}
	var bc struct {
		Deepseek struct {
			FundedKey string `json:"funded_key"`
		} `json:"deepseek"`
	}
	if err := json.Unmarshal(cfgRaw, &bc); err != nil || bc.Deepseek.FundedKey == "" {
		res.add("funded_key_configured", false, "config.json deepseek.funded_key missing or unparsable")
		return res
	}

	// ~/.claude/settings.json 的 env 块（claude 原生文件通道）
	claudeDir := filepath.Join(root, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		res.add("claude_dir", false, "%v", err)
		return res
	}
	settings := map[string]any{"env": map[string]any{
		"ANTHROPIC_BASE_URL":         "https://api.deepseek.com/anthropic",
		"ANTHROPIC_AUTH_TOKEN":       bc.Deepseek.FundedKey,
		"ANTHROPIC_MODEL":            "deepseek-chat",
		"ANTHROPIC_SMALL_FAST_MODEL": "deepseek-chat",
	}}
	settingsJSON, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), settingsJSON, 0o600); err != nil {
		res.add("settings_seed", false, "%v", err)
		return res
	}

	srv, srvURL := newFixtureMailServer([]MailSummary{{
		ID: "01FIXTURE0000000000000000X", From: "actor@fixture.test",
		Subject: "reply with the single word: ok",
		Preview: "Trivial task: answer with one word.",
		Unread:  true, ReceivedAt: time.Now().Unix(),
	}})
	defer srv.Close()

	cfg := fmt.Sprintf(`{
  "server": %q, "poll_interval_sec": 5, "timeout_sec": 420,
  "agents": [{"address":%q,"password":"bench-fixture-pw","cli":"claude","workdir":%q,"model":"deepseek-chat"}]
}`, srvURL, acctA, filepath.Join(env.RunDir, "wd-alpha"))
	cfgPath := filepath.Join(env.RunDir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		res.add("config", false, "%v", err)
		return res
	}

	cmd := exec.Command(env.WorkerBin, "-config", cfgPath)
	cmd.Env = WhitelistEnv(root)
	cmd.Dir = env.RunDir
	logBuf := &bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = logBuf, logBuf

	_ = env.TL.Add("note", "s12 start (real claude x funded deepseek, settings.json channel)", nil)
	if err := cmd.Start(); err != nil {
		res.add("spawn", false, "%v", err)
		return res
	}

	// 观察窗：轮询 state 文件绑定（顶层 session_id）。claude-code 系统
	// 提示词重+真生成，实测 >200s——预算 300s（CLI 硬顶 420s）。
	statePath := filepath.Join(env.RunDir, "config.alpha.state.json")
	deadline := time.Now().Add(300 * time.Second)
	bound := false
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			res.add("budget", false, "budget hit during observation window")
			_ = cmd.Process.Kill()
			return res
		default:
		}
		if b, err := os.ReadFile(statePath); err == nil && strings.Contains(string(b), "session_id") &&
			!strings.Contains(string(b), `"session_id": ""`) {
			bound = true
			break
		}
		time.Sleep(2 * time.Second)
	}
	alive := cmd.Process.Signal(syscall.Signal(0)) == nil
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	log := logBuf.String()
	logPath := filepath.Join(env.RunDir, "s12-worker.log")
	_ = os.WriteFile(logPath, []byte(log), 0o644)
	_ = env.TL.Add("evidence", "s12 worker log", map[string]any{"bytes": len(log), "path": logPath})

	state, stErr := os.ReadFile(statePath)
	bound = bound && stErr == nil && strings.Contains(string(state), "session_id") &&
		!strings.Contains(string(state), `"session_id": ""`)
	res.add("session_bound", bound,
		"state file carries a bound session within the window (err=%v, %d bytes)", stErr, len(state))

	if _, err := os.Stat(filepath.Join(env.RunDir, "errors-alpha.log")); os.IsNotExist(err) {
		res.add("no_error_archived", true, "no errors-alpha.log (clean success)")
	} else {
		res.add("no_error_archived", false, "errors-alpha.log exists on the success path")
	}

	res.add("no_wake_failed", !strings.Contains(log, "wake failed"),
		"log carries no wake-failed line")
	res.add("worker_survives", alive, "worker alive through the real wake")
	return res
}

func init() { register(s12claudereal{}) }
