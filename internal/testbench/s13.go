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

// s13 — pi 真成功路径（opt-in: TESTBENCH_REAL=1）：真 pi-coding-agent × 真
// deepseek 有余额 key（K1）。四家 CLI 真弹矩阵的最后一块（opencode s8/s10、
// codex s11、claude s12、pi 本场景）。邮件面仍走 fixture（控变量：真
// LLM、假邮件）。
//
// pi 接线（0.73.1 实测）：PI_CODING_AGENT_DIR 环境变量重定向 agent 目录
// （models.json 自定义 provider: baseUrl=deepseek OpenAI 兼容 /v1 +
// apiKey 内联=文件通道铁律 ✓；api=openai-completions）；worker piAdapter
// 调 `pi -p --mode json --session-dir <workdir>/.pi-sessions --model
// deepseek/deepseek-chat`，首事件 {"type":"session","id"} 即绑定锚。
// 冒烟实录：回复 "ok"，usage input=391 output=1。
//
// 断言同 s10：①会话绑定 ②零错误归档 ③无 wake failed/存活 ④真采帧。
type s13pireal struct{}

func (s13pireal) Name() string { return "s13-pi-real-success" }
func (s13pireal) Desc() string {
	return "s13 pi 真成功路径（opt-in TESTBENCH_REAL=1）：真 pi×有余额 key，会话绑定+零错误"
}
func (s13pireal) Timeout() time.Duration { return 10 * time.Minute }

func (s s13pireal) Run(ctx context.Context, env *Env) Result {
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

	// pi agent 目录（PI_CODING_AGENT_DIR 重定向进台架根）: models.json
	// 自定义 provider，key 内联=文件通道（0600）。env 面 /proc 零密钥。
	agentDir := filepath.Join(root, ".pi-agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		res.add("agent_dir", false, "%v", err)
		return res
	}
	modelsJSON, _ := json.Marshal(map[string]any{
		"providers": map[string]any{
			"deepseek": map[string]any{
				"name":    "DeepSeek",
				"baseUrl": "https://api.deepseek.com/v1",
				"apiKey":  bc.Deepseek.FundedKey,
				"api":     "openai-completions",
				"models": []map[string]any{{
					"id":            "deepseek-chat",
					"name":          "DeepSeek Chat",
					"contextWindow": 131072,
					"maxTokens":     8192,
				}},
			},
		},
	})
	modelsPath := filepath.Join(agentDir, "models.json")
	if err := os.WriteFile(modelsPath, modelsJSON, 0o600); err != nil {
		res.add("models_seed", false, "%v", err)
		return res
	}
	res.add("models_seeded", true, "models.json seeded at %s (0600, key inline=file channel)", modelsPath)

	srv, srvURL := newFixtureMailServer([]MailSummary{{
		ID: "01FIXTURE0000000000000000X", From: "actor@fixture.test",
		Subject: "reply with the single word: ok",
		Preview: "Trivial task: answer with one word.",
		Unread:  true, ReceivedAt: time.Now().Unix(),
	}})
	defer srv.Close()

	wd := filepath.Join(env.RunDir, "wd-alpha")
	cfg := fmt.Sprintf(`{
  "server": %q, "poll_interval_sec": 5, "timeout_sec": 480,
  "agents": [{"address":%q,"password":"bench-fixture-pw","cli":"pi","workdir":%q,
    "model":"deepseek/deepseek-chat",
    "env":{"PI_CODING_AGENT_DIR":%q}}]
}`, srvURL, acctA, wd, agentDir)
	cfgPath := filepath.Join(env.RunDir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		res.add("config", false, "%v", err)
		return res
	}

	// WORKER_TUI_DUMP: real-run TUI frames (bench acceptance artifacts).
	framesDir := filepath.Join(env.RunDir, "frames")
	cmd := exec.Command(env.WorkerBin, "-config", cfgPath)
	cmd.Env = WhitelistEnv(root, "WORKER_TUI_DUMP="+framesDir)
	cmd.Dir = env.RunDir
	logBuf := &bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = logBuf, logBuf

	_ = env.TL.Add("note", "s13 start (real pi x funded deepseek, model=deepseek/deepseek-chat)", nil)
	if err := cmd.Start(); err != nil {
		res.add("spawn", false, "%v", err)
		return res
	}
	// 观察窗与 CLI 预算对齐（0910 定式：慢≠坏，窗口先掐=假失败）。
	// pi 首轮是全工具真探索，实测可超 5 分钟——预算 480s 给足绳子。
	statePath := filepath.Join(env.RunDir, "config.alpha.state.json")
	deadline := time.Now().Add(480 * time.Second)
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
	logPath := filepath.Join(env.RunDir, "s13-worker.log")
	_ = os.WriteFile(logPath, []byte(log), 0o644)
	_ = env.TL.Add("evidence", "s13 worker log", map[string]any{"bytes": len(log), "path": logPath})

	// ① 会话绑定：唤醒成功的硬证据（worker 静默成功，盘面说话）。
	state, stErr := os.ReadFile(statePath)
	bound = bound && stErr == nil && strings.Contains(string(state), "session_id") &&
		!strings.Contains(string(state), `"session_id": ""`)
	res.add("session_bound", bound,
		"state file carries a bound session within the window (err=%v, %d bytes)", stErr, len(state))

	// ② 零错误归档：真实成功路径不能有失败面。
	if _, err := os.Stat(filepath.Join(env.RunDir, "errors-alpha.log")); os.IsNotExist(err) {
		res.add("no_error_archived", true, "no errors-alpha.log (clean success)")
	} else {
		res.add("no_error_archived", false, "errors-alpha.log exists on the success path")
	}

	// ③ worker 存活且日志无 wake failed。
	res.add("no_wake_failed", !strings.Contains(log, "wake failed"),
		"log carries no wake-failed line")
	res.add("worker_survives", alive, "worker alive through the real wake")

	// ④ 真实运行帧（boss 验收）。
	frames, _ := os.ReadDir(filepath.Join(env.RunDir, "frames"))
	res.add("real_frames_captured", len(frames) > 0, "%d real TUI frame(s) dumped", len(frames))
	return res
}

func init() { register(s13pireal{}) }
