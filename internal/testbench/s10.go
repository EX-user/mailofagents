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

// s10 — 真成功路径（opt-in: TESTBENCH_REAL=1）：真 opencode × 真 deepseek
// 有余额 key（K1）。s8 钉"真失败"，本场景钉"真成功"——真实模型真的在
// 处理邮件，而不是假件宣告的机械面。邮件面仍走 fixture（控变量：真处
// LLM、假邮件）。
//
// 断言（成功在 worker 里是静默的——无 log 行，证据在盘面）：
//   ①会话绑定：state 文件落 session_id（唤醒成功的硬证据）；
//   ②零错误归档：errors-alpha.log 不存在（无失败）；
//   ③唤醒在真实时延内完成（fake 件秒回，真模型秒级~数十秒——timeout
//     120s/窗口 90s）。
//
// key 通道同 s8：config.json(deepseek.funded_key) → bench root auth.json
// (0600)，env 面零密钥。成本：每轮一次真实小请求（"reply ok" 级）。

type s10realsuccess struct{}

func (s10realsuccess) Name() string { return "s10-real-success" }
func (s10realsuccess) Desc() string {
	return "s10 真成功路径（opt-in TESTBENCH_REAL=1）：真 opencode×有余额 key，会话绑定+零错误"
}
func (s10realsuccess) Timeout() time.Duration { return 4 * time.Minute }

func (s s10realsuccess) Run(ctx context.Context, env *Env) Result {
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
	authDir := filepath.Join(root, ".local", "share", "opencode")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		res.add("auth_dir", false, "%v", err)
		return res
	}
	// 同一 auth.json 槽位：s8 写零余额、s10 写有余额——两场景互斥跑
	// （opt-in 单场景触发），不构成共享状态。
	authJSON, _ := json.Marshal(map[string]any{
		"deepseek": map[string]any{"type": "api", "key": bc.Deepseek.FundedKey},
	})
	if err := os.WriteFile(filepath.Join(authDir, "auth.json"), authJSON, 0o600); err != nil {
		res.add("auth_seed", false, "%v", err)
		return res
	}

	modelID := ""
	if out, merr := exec.Command("opencode", "models").Output(); merr == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "deepseek/") {
				modelID = strings.TrimSpace(strings.Fields(line)[0])
				break
			}
		}
	}
	res.add("deepseek_in_catalog", modelID != "", "first in-catalog model: %q", modelID)
	if modelID == "" {
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
  "agents": [{"address":%q,"password":"x","cli":"opencode","workdir":%q,"model":%q}]
}`, srvURL, acctA, filepath.Join(env.RunDir, "wd-alpha"), modelID)
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

	_ = env.TL.Add("note", "s10 start (real opencode x funded deepseek, model="+modelID+")", nil)
	if err := cmd.Start(); err != nil {
		res.add("spawn", false, "%v", err)
		return res
	}
	// 观察窗：轮询 state 文件出现（真实生成时延秒级~分钟级——引导模板
	// token 量大，90s 实测不够）。至多 ~200s，超时按失败诊断。
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
	logPath := filepath.Join(env.RunDir, "s10-worker.log")
	_ = os.WriteFile(logPath, []byte(log), 0o644)
	_ = env.TL.Add("evidence", "s10 worker log", map[string]any{"bytes": len(log), "path": logPath})

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
	return res
}

func init() { register(s10realsuccess{}) }
