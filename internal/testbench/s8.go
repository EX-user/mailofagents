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
	"time"
)

// S8 — 真失败形态（opt-in: TESTBENCH_REAL=1 时才实跑）：真 opencode × 真
// deepseek 零余额 key，邮件面保持 fixture（假件只让位于"失败是真"这一件事）。
//
// 0905 首猎即中：真实措辞是「Insufficient Balance」——不含 quota/429 token，
// 旧 shortErr 分类器整类漏配（假件 insufficient_quota 文案反而能中）。
// 本场景与 shortErr 刀双向钉死：
//   ① 真形态如实归档：errors-alpha.log 带 "Insufficient Balance" 原文；
//   ② 分类归位：板上 wake failed 行 = provider quota/429；
//   ③ 退避有界：窗口内真实调用数不超界（不自旋打 provider）。
//
// key 通道走铁律：config.json(deepseek.zero_key) → bench root auth.json
// (0600)，全程不进 env 面（/proc 扫描口径不变）。
//
// 模型 id 动态发现（opencode models 首个 deepseek 行）——deepseek-chat 一类
// 目录外 id 会先吃 UnknownError，那是错模型错不是余额错，不构成本场景证据。

type s8realquota struct{}

func (s8realquota) Name() string { return "s8-real-quota-form" }
func (s8realquota) Desc() string {
	return "S8 真失败形态（opt-in TESTBENCH_REAL=1）：真 opencode×零余额 key，真实措辞归档+分类钉+退避界"
}
func (s8realquota) Timeout() time.Duration { return 3 * time.Minute }

func (s s8realquota) Run(ctx context.Context, env *Env) Result {
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

	// key 通道：config → bench root auth.json（0600，不入 env 面）。
	cfgRaw, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		res.add("bench_config", false, "read %s: %v", filepath.Join(root, "config.json"), err)
		return res
	}
	var bc struct {
		Deepseek struct {
			ZeroKey string `json:"zero_key"`
		} `json:"deepseek"`
	}
	if err := json.Unmarshal(cfgRaw, &bc); err != nil || bc.Deepseek.ZeroKey == "" {
		res.add("zero_key_configured", false, "config.json deepseek.zero_key missing or unparsable")
		return res
	}
	authDir := filepath.Join(root, ".local", "share", "opencode")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		res.add("auth_dir", false, "%v", err)
		return res
	}
	authJSON, _ := json.Marshal(map[string]any{
		"deepseek": map[string]any{"type": "api", "key": bc.Deepseek.ZeroKey},
	})
	if err := os.WriteFile(filepath.Join(authDir, "auth.json"), authJSON, 0o600); err != nil {
		res.add("auth_seed", false, "%v", err)
		return res
	}

	// 动态发现在册 deepseek 模型（目录外 id 会吃 UnknownError，证据失真）。
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
		Subject: fixtureSubject, Preview: fixturePreview,
		Unread: true, ReceivedAt: time.Now().Unix(),
	}})
	defer srv.Close()

	cfg := fmt.Sprintf(`{
  "server": %q, "poll_interval_sec": 1, "timeout_sec": 30,
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

	_ = env.TL.Add("note", "s8 start (real opencode x zero-balance deepseek, model="+modelID+")", nil)
	if err := cmd.Start(); err != nil {
		res.add("spawn", false, "%v", err)
		return res
	}
	// 观察窗 ~30s：零余额拒绝是秒级往返，足够多轮真实调用。
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			res.add("budget", false, "budget hit during observation window")
			_ = cmd.Process.Kill()
			return res
		case <-time.After(200 * time.Millisecond):
		}
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	log := logBuf.String()
	logPath := filepath.Join(env.RunDir, "s8-worker.log")
	_ = os.WriteFile(logPath, []byte(log), 0o644)
	_ = env.TL.Add("evidence", "s8 worker log", map[string]any{"bytes": len(log), "path": logPath})

	// ① 真形态如实归档：errors 文件带 provider 原文（真措辞，非假件文案）。
	errLog, errErr := os.ReadFile(filepath.Join(env.RunDir, "errors-alpha.log"))
	res.add("real_form_archived", errErr == nil && strings.Contains(string(errLog), "Insufficient Balance"),
		"errors-alpha.log carries the REAL provider wording (err=%v, %d bytes)", errErr, len(errLog))

	// ② 分类归位（shortErr insufficient 刀）：板上行 = provider quota/429。
	res.add("classified_quota", strings.Contains(log, "wake failed: provider quota/429"),
		"log classifies the real failure as provider quota/429")

	// ③ 退避有界：真实调用数（错误归档行数计）不超界——零余额拒绝秒级
	// 往返，30s 窗 poll 1s 的理论上限 ~31；自旋/放大形态会远超此界。
	calls := strings.Count(string(errLog), "Insufficient Balance")
	res.add("backoff_bounded", calls <= 45, "real invocations in ~30s window = %d (poll-cadence bound 45)", calls)
	return res
}

func init() { register(s8realquota{}) }
