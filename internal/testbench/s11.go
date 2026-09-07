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

// s11 — codex 真成功路径（opt-in: TESTBENCH_REAL=1）：真 codex × 真
// deepseek 有余额 key（responses wire）。仿 s10：邮件面 fixture，真模型
// 真处理一封邮件，成功在 worker 里静默——证据在盘面。
//
// 接线（REALRUN_RESEARCH.md §2，alice 裁准 2026-09-07）：
//   - ~/.codex/config.toml：provider base_url=deepseek OpenAI 兼容端点，
//     wire_api="responses"（0.153+ 已弃 chat；deepseek /v1/responses 实测 200）。
//   - key 通道：worker 子进程 wrapper 从 bench config 读 key 后 export
//     DEEPSEEK_API_KEY 再 exec 真 codex——key 不进 worker environ
//     （/proc 扫描面口径不变）。
//   - 模型名写死 deepseek-chat（responses 端点名；勿用 opencode 目录的
//     v4 命名——S8 教训：目录外 id 污染证据）。models.json 缺失仅降级警告。
//
// 断言（仿 s10）：①state 绑定 thread_id ②零错误归档 ③零 wake failed
// ④存活。实测时延 ~30s/轮。

type s11codexreal struct{}

func (s11codexreal) Name() string { return "s11-codex-real-success" }
func (s11codexreal) Desc() string {
	return "s11 codex 真成功路径（opt-in TESTBENCH_REAL=1）：真 codex×有余额 key（responses wire），会话绑定+零错误"
}
func (s11codexreal) Timeout() time.Duration { return 4 * time.Minute }

const s11configTOML = `model = "deepseek-chat"
model_provider = "deepseek"

[model_providers.deepseek]
name = "deepseek"
base_url = "https://api.deepseek.com/v1"
env_key = "DEEPSEEK_API_KEY"
wire_api = "responses"
`

func (s s11codexreal) Run(ctx context.Context, env *Env) Result {
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

	// ~/.codex/config.toml（bench HOME 下的文件通道）
	codexDir := filepath.Join(root, ".codex")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		res.add("codex_dir", false, "%v", err)
		return res
	}
	if err := os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte(s11configTOML), 0o600); err != nil {
		res.add("config_toml", false, "%v", err)
		return res
	}

	// key wrapper（alice 裁准通道）：config(0600) → wrapper 内 export → exec。
	// codex 必须写死绝对路径：wrapDir 在 PATH 首位时，wrapper 内 exec codex
	// 会解析回 wrapper 自身形成无限循环（实测自摆乌龙二号）。
	codexPath, err := exec.LookPath("codex")
	if err != nil {
		res.add("codex_installed", false, "codex not on host PATH (Linux build required)")
		return res
	}
	wrapDir := filepath.Join(env.RunDir, "wrappers")
	if err := os.MkdirAll(wrapDir, 0o755); err != nil {
		res.add("wrap_dir", false, "%v", err)
		return res
	}
	wrap := fmt.Sprintf(`#!/bin/bash
KEY=$(python3 -c 'import json;print(json.load(open("%s"))["deepseek"]["funded_key"])' 2>/dev/null)
export DEEPSEEK_API_KEY="$KEY"
exec %s "$@"
`, filepath.Join(root, "config.json"), codexPath)
	if err := os.WriteFile(filepath.Join(wrapDir, "codex"), []byte(wrap), 0o755); err != nil {
		res.add("wrapper", false, "%v", err)
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
  "server": %q, "poll_interval_sec": 5, "timeout_sec": 300,
  "agents": [{"address":%q,"password":"x","cli":"codex","workdir":%q,"model":"deepseek-chat"}]
}`, srvURL, acctA, filepath.Join(env.RunDir, "wd-alpha"))
	cfgPath := filepath.Join(env.RunDir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		res.add("config", false, "%v", err)
		return res
	}

	cmd := exec.Command(env.WorkerBin, "-config", cfgPath)
	cmd.Env = WhitelistEnv(root,
		"PATH="+wrapDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	cmd.Dir = env.RunDir
	logBuf := &bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = logBuf, logBuf

	_ = env.TL.Add("note", "s11 start (real codex x funded deepseek, responses wire)", nil)
	if err := cmd.Start(); err != nil {
		res.add("spawn", false, "%v", err)
		return res
	}

	// 观察窗：轮询 state 文件绑定（thread_id）。实测 ~30s，至多 ~200s。
	statePath := filepath.Join(env.RunDir, "config.alpha.state.json")
	deadline := time.Now().Add(200 * time.Second)
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
	logPath := filepath.Join(env.RunDir, "s11-worker.log")
	_ = os.WriteFile(logPath, []byte(log), 0o644)
	_ = env.TL.Add("evidence", "s11 worker log", map[string]any{"bytes": len(log), "path": logPath})

	state, stErr := os.ReadFile(statePath)
	bound = bound && stErr == nil && strings.Contains(string(state), "session_id") &&
		!strings.Contains(string(state), `"session_id": ""`)
	res.add("session_bound", bound,
		"state file carries a bound thread id within the window (err=%v, %d bytes)", stErr, len(state))

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

func init() { register(s11codexreal{}) }
