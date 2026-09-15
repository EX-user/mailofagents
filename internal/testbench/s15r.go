package testbench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// s15r — 真弹并行度（opt-in: TESTBENCH_REAL=1，boss 指令 0912「并发度测试
// 可用真cli测试」）：两个真 opencode × 真 deepseek key，独立 workdir + 独立
// XDG_DATA_HOME，同一 tick 收信同时唤醒。shim 记录每账户真实 CLI 进程的
// start/end 时间戳（41k 证据同 s15），断言：
//   ① 真 CLI 进程区间重叠（并行而非串行阶梯）；
//   ② 双会话各自绑定（state 全地址命名）；
//   ③ 零错误归档。
// 起跑延迟与时长入时间线（真 CLI 冷启动+上游首字时延的长期趋势）。

type s15rrealparallel struct{}

func (s15rrealparallel) Name() string { return "s15r-real-parallel" }
func (s15rrealparallel) Desc() string {
	return "s15r 真弹并行度（opt-in TESTBENCH_REAL=1）：双真 opencode 同时唤醒，区间重叠+双绑定+零错误"
}
func (s15rrealparallel) Timeout() time.Duration { return 10 * time.Minute }

func (s s15rrealparallel) Run(ctx context.Context, env *Env) Result {
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
	realBin, err := exec.LookPath("opencode")
	if err != nil {
		res.add("real_opencode_present", false, "opencode not on PATH: %v", err)
		return res
	}
	res.add("real_opencode_present", true, "%s", realBin)

	root := env.Root()
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
		res.add("funded_key_configured", false, "config.json deepseek.funded_key missing")
		return res
	}

	// 模型钉（0912：内置目录 flash 档 UnknownError 连发，chat-completions
	// 稳定）——opencode.json 自定义 provider 摆脱远程注册表漂移。
	modelID := "deepseek-custom/deepseek-chat"
	res.add("model_pinned", true, "custom provider model: %q", modelID)

	// 每账户独立 XDG 数据目录（并行的前提）：各自 auth.json 种子（文件通道）。
	srv, srvURL := s15OneShotServer()
	defer srv.Close()

	binDir := filepath.Join(env.RunDir, "fakebin")
	tl := filepath.Join(env.RunDir, "cli-timeline.log")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		res.add("fakebin", false, "%v", err)
		return res
	}
	shim := fmt.Sprintf("#!/bin/bash\n"+
		"echo \"start $FAKE_TAG $(date +%%s.%%N)\" >> \"$S15_TIMELINE\"\n"+
		"%s \"$@\"\n"+
		"st=$?\n"+
		"echo \"end $FAKE_TAG $(date +%%s.%%N)\" >> \"$S15_TIMELINE\"\n"+
		"exit $st\n", realBin)
	if err := os.WriteFile(filepath.Join(binDir, "opencode"), []byte(shim), 0o755); err != nil {
		res.add("shim", false, "%v", err)
		return res
	}

	agents := []struct {
		addr, tag, xdg, wd string
	}{
		{"r1@fixture.test", "real1", filepath.Join(env.RunDir, "xdg-1"), filepath.Join(env.RunDir, "wd-1")},
		{"r2@fixture.test", "real2", filepath.Join(env.RunDir, "xdg-2"), filepath.Join(env.RunDir, "wd-2")},
	}
	authJSON, _ := json.Marshal(map[string]any{
		"deepseek": map[string]any{"type": "api", "key": bc.Deepseek.FundedKey},
	})
	// opencode.json 自定义 provider（chat-completions + deepseek-chat）：
	// 与内置注册表漂移解耦（0912 实测内置 flash 档 UnknownError 连发而
	// chat-completions 稳定）——与 pi 的 models.json 同构。
	customProvider := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"provider": map[string]any{
			"deepseek-custom": map[string]any{
				"npm":     "@ai-sdk/openai-compatible",
				"name":    "DeepSeek Custom",
				"options": map[string]any{"baseURL": "https://api.deepseek.com/v1", "apiKey": bc.Deepseek.FundedKey},
				"models":  map[string]any{"deepseek-chat": map[string]any{"name": "DeepSeek Chat"}},
			},
		},
	}
	customJSON, _ := json.Marshal(customProvider)

	var agentJSON []string
	for i, a := range agents {
		authDir := filepath.Join(a.xdg, "opencode")
		if err := os.MkdirAll(authDir, 0o700); err != nil {
			res.add("auth_dir", false, "%v", err)
			return res
		}
		if err := os.WriteFile(filepath.Join(authDir, "auth.json"), authJSON, 0o600); err != nil {
			res.add("auth_seed", false, "%v", err)
			return res
		}
		cfgDir := filepath.Join(env.RunDir, fmt.Sprintf("xcfg-%d", i+1), "opencode")
		if err := os.MkdirAll(cfgDir, 0o700); err != nil {
			res.add("cfg_dir", false, "%v", err)
			return res
		}
		if err := os.WriteFile(filepath.Join(cfgDir, "opencode.json"), customJSON, 0o600); err != nil {
			res.add("provider_seed", false, "%v", err)
			return res
		}
		agentJSON = append(agentJSON, fmt.Sprintf(
			`{"address":%q,"password":"bench-fixture-pw","cli":"opencode","workdir":%q,
			  "model":%q,
			  "env":{"FAKE_TAG":%q,"S15_TIMELINE":%q,"XDG_DATA_HOME":%q,"XDG_CONFIG_HOME":%q}}`,
			a.addr, a.wd, modelID, a.tag, tl, a.xdg, filepath.Join(env.RunDir, fmt.Sprintf("xcfg-%d", i+1))))
		_ = i
	}
	cfg := fmt.Sprintf(`{
  "server": %q, "poll_interval_sec": 1, "timeout_sec": 480,
  "agents": [%s]
}`, srvURL, strings.Join(agentJSON, ","))
	cfgPath := filepath.Join(env.RunDir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		res.add("config", false, "%v", err)
		return res
	}

	cmd := exec.Command(env.WorkerBin, "-config", cfgPath)
	cmd.Env = WhitelistEnv(root,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd.Dir = env.RunDir
	logBuf := &bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = logBuf, logBuf

	_ = env.TL.Add("note", "s15r start (two real opencode x funded deepseek, isolated XDG)", nil)
	if err := cmd.Start(); err != nil {
		res.add("spawn", false, "%v", err)
		return res
	}
	// 双真弹并行：观察窗与 CLI 预算对齐（480s）。
	deadline := time.Now().Add(480 * time.Second)
	bound := 0
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			res.add("budget", false, "budget hit during observation")
			_ = cmd.Process.Kill()
			return res
		case <-time.After(2 * time.Second):
		}
		bound = 0
		for _, a := range agents {
			sp := filepath.Join(env.RunDir, fmt.Sprintf("config.%s.state.json", s15AddrToken(a.addr)))
			if b, err := os.ReadFile(sp); err == nil && strings.Contains(string(b), "session_id") &&
				!strings.Contains(string(b), `"session_id": ""`) {
				bound++
			}
		}
		if bound == len(agents) {
			break
		}
	}
	alive := cmd.Process.Signal(syscall.Signal(0)) == nil
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	log := logBuf.String()
	logPath := filepath.Join(env.RunDir, "s15r-worker.log")
	_ = os.WriteFile(logPath, []byte(log), 0o644)
	_ = env.TL.Add("evidence", "s15r worker log", map[string]any{"bytes": len(log), "path": logPath})

	// ① 真 CLI 进程区间重叠（并行核心断言）
	spans := map[string][2]float64{}
	data, _ := os.ReadFile(tl)
	starts := map[string]float64{}
	for _, ln := range strings.Split(string(data), "\n") {
		f := strings.Fields(ln)
		if len(f) != 3 || (f[0] != "start" && f[0] != "end") {
			continue
		}
		var ts float64
		if _, err := fmt.Sscanf(f[2], "%f", &ts); err != nil {
			continue
		}
		if f[0] == "start" {
			starts[f[1]] = ts
			continue
		}
		if s0, ok := starts[f[1]]; ok {
			spans[f[1]] = [2]float64{s0, ts}
		}
	}
	res.add("real_cli_spans", len(spans) == len(agents), "want %d spans, got %d", len(agents), len(spans))
	if s1v, ok := spans["real1"]; ok {
		if s2v, ok := spans["real2"]; ok {
			overlap := s1v[0] <= s2v[1] && s2v[0] <= s1v[1]
			res.add("real_wakes_overlap", overlap,
				"real1[%.1f..%.1f] real2[%.1f..%.1f] overlap=%v",
				s1v[0], s1v[1], s2v[0], s2v[1], overlap)
			dur := []float64{s1v[1] - s1v[0], s2v[1] - s2v[0]}
			sort.Float64s(dur)
			_ = env.TL.Add("note", fmt.Sprintf("s15r durations: %.1fs / %.1fs", dur[0], dur[1]), nil)
			res.Notes = append(res.Notes, fmt.Sprintf("real wake durations %.1fs / %.1fs", dur[0], dur[1]))
		}
	}

	// ② 双会话绑定
	res.add("both_sessions_bound", bound == len(agents),
		"bound %d/%d accounts within the window", bound, len(agents))

	// ③ 零错误归档
	errFree := true
	for _, a := range agents {
		if _, err := os.Stat(filepath.Join(env.RunDir, fmt.Sprintf("errors-%s.log", s15LocalPart(a.addr)))); err == nil {
			errFree = false
		}
	}
	res.add("no_error_archived", errFree, "no errors-*.log on the parallel success path")

	res.add("worker_survives", alive, "worker alive through the parallel real wakes")
	return res
}

func init() { register(s15rrealparallel{}) }

func s15AddrToken(addr string) string {
	if i := strings.Index(addr, "@"); i > 0 {
		return addr[:i]
	}
	return addr
}

func s15LocalPart(addr string) string {
	if i := strings.Index(addr, "@"); i > 0 {
		return addr[:i]
	}
	return addr
}
