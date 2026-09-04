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

// S2 — 额度耗尽（key 故障族）：双账户单 worker——alpha 的假 CLI 注入
// 「额度耗尽」失败面（exit 42 + quota 错误文本），bravo 健康。断言：
//   ① 错误如实归档：errors-alpha.log 落盘且含注入的错误原文；
//   ② shortErr 分类正确：worker 日志的 wake failed 行带 exit code 与
//      stderr 尾（错误类别可从日志一眼判定，不再 <nil>）；
//   ③ 退避不自旋：失败不放大调用频率（窗口内调用数 ≤ poll 节拍+余量）；
//   ④ 他账户零受累：bravo 照常唤醒/绑定会话，无错误文件、无串扰。
//
// 真实形态对照：0 余额 key 时 CLI 在模型调用步报 insufficient_quota——
// 假 CLI 以同构错误文本+非零退出模拟，机制面等价，无需真 key。

type s2quota struct{}

func (s2quota) Name() string { return "s2-quota-exhaustion" }
func (s2quota) Desc() string {
	return "S2 额度耗尽：错误如实归档/shortErr 分类/退避不自旋/他账户零受累"
}
func (s2quota) Timeout() time.Duration { return 3 * time.Minute }

const quotaErrText = "insufficient_quota: You exceeded your current quota, please check your plan and billing details (402)"

func (s s2quota) Run(ctx context.Context, env *Env) Result {
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
	evFile := filepath.Join(evDir, "s2-calls.txt")

	cfg := fmt.Sprintf(`{
  "server": %q, "poll_interval_sec": 1, "timeout_sec": 30,
  "agents": [
    {"address":%q,"password":"x","cli":"opencode","workdir":%q,
     "env": {"BENCH_FAIL": "insufficient_quota"}},
    {"address":%q,"password":"x","cli":"opencode","workdir":%q}
  ]
}`, srvURL, acctA, filepath.Join(env.RunDir, "wd-alpha"),
		acctB, filepath.Join(env.RunDir, "wd-bravo"))
	cfgPath := filepath.Join(env.RunDir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		res.add("config", false, "%v", err)
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

	_ = env.TL.Add("note", "s2 start (alpha=failing CLI, bravo=healthy)", nil)
	if err := cmd.Start(); err != nil {
		res.add("spawn", false, "%v", err)
		return res
	}

	// 观察窗：固定 ~7s（poll 1s → alpha 预期 ~6 次失败调用，bravo 同拍健康轮）
	deadline := time.Now().Add(7 * time.Second)
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
	_ = env.TL.Add("evidence", "s2 worker log", map[string]int{"bytes": len(log)})

	// ① 错误如实归档
	errLog, errErr := os.ReadFile(filepath.Join(env.RunDir, "errors-alpha.log"))
	res.add("error_archived", errErr == nil && strings.Contains(string(errLog), "insufficient_quota"),
		"errors-alpha.log exists and carries the injected quota text (err=%v, %d bytes)", errErr, len(errLog))

	// ② shortErr 分类正确：quota 类失败被归入 provider quota/429 类别
	//   （分类器语义：板上人读类别，原始证据在 errors 文件——①已断言）
	res.add("shorterr_quota_class", strings.Contains(log, "wake failed: provider quota/429"),
		"log classifies the failure as provider quota/429")

	// ③ 退避不自旋：alpha 调用数 ≤ 窗口/poll + 余量
	ev, _ := os.ReadFile(evFile)
	alphaCalls := strings.Count(string(ev), "wd-alpha") / 2 // 每次 argv+dir 双处提及
	res.add("backoff_bounded", alphaCalls <= 10, "alpha invocations in ~7s window = %d (poll=1s, bound 10)", alphaCalls)

	// ④ 他账户零受累
	stB, errB2 := os.ReadFile(filepath.Join(env.RunDir, "config.bravo.state.json"))
	res.add("bravo_healthy", errB2 == nil && strings.Contains(string(stB), sess),
		"bravo bound its session normally during alpha's outage")
	if _, err := os.Stat(filepath.Join(env.RunDir, "errors-bravo.log")); os.IsNotExist(err) {
		res.add("bravo_no_errors", true, "no error file for bravo")
	} else {
		res.add("bravo_no_errors", false, "errors-bravo.log unexpectedly exists")
	}
	// 附加：失败面同样走 salvage→resume 链（真实 CLI 语义：先宣告后失败）
	blocks := strings.Split(string(ev), "=== CALL ===")
	var alphaBlocks []string
	for _, b := range blocks {
		if strings.Contains(b, "wd-alpha") {
			alphaBlocks = append(alphaBlocks, b)
		}
	}
	if len(alphaBlocks) >= 2 {
		second := alphaBlocks[1]
		res.add("alpha_resumes_after_fail", strings.Contains(second, "-- -s") && strings.Contains(second, "-- "+sess),
			"alpha's second wake resumes the salvaged session despite failures")
	} else {
		res.add("alpha_resumes_after_fail", false, "want >=2 alpha invocations, got %d", len(alphaBlocks))
	}
	return res
}

func init() { register(s2quota{}) }
