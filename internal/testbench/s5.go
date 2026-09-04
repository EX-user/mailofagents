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

// S5 — 长闲置批量信：积压 12 封同时到达（超「仅列最新 10」阈值）。
// 断言 digest 的批量语义：
//   ① 计数行如实（12 未读）+ 截断提示行在（「其余 2 封更早未读未列出」）；
//   ② 完整性：最新 10 封主题逐封在 payload 里，第 11/12 封不在（截断）；
//   ③ 顺序：新→旧（第一个条目=最新主题，第 10 个=第 10 新）；
//   ④ 编号完整：[未读 1/12]…[未读 10/12]。
//
// 复用面：真实 wake 管道 + 假 CLI 证据（S0 同款），无真 key。

type s5batch struct{}

func (s5batch) Name() string { return "s5-batch-mail" }
func (s5batch) Desc() string {
	return "S5 长闲置批量信：12 封积压→计数/截断/顺序/完整性断言"
}
func (s5batch) Timeout() time.Duration { return 3 * time.Minute }

func (s s5batch) Run(ctx context.Context, env *Env) Result {
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

	// 12 封：主题 S01(最旧)…S12(最新)，received_at 升序（新邮件 id/时间更大）。
	const total = 12
	// 合同：inbox 新→旧返回。fixture 必须遵守（worker 信任服务端排序取前 10）。
	mails := make([]MailSummary, 0, total)
	for i := total; i >= 1; i-- {
		mails = append(mails, MailSummary{
			ID:      fmt.Sprintf("01FIXTURE%02d0000000000000000X", i),
			From:    fmt.Sprintf("sender%02d@fixture.test", i),
			Subject: fmt.Sprintf("积压信 S%02d：分片组块确认", i),
			Preview: fmt.Sprintf("第 %02d 封积压信正文预览，落树位就绪。", i),
			Unread:  true,
			// S12 最新：基准时间 + i 分钟
			ReceivedAt: time.Now().Add(time.Duration(i) * time.Minute).Unix(),
		})
	}
	mailHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"messages": mails, "count": total, "total_count": total, "unread_count": total,
		})
	})
	srv := &http.Server{Handler: mailHandler, ReadHeaderTimeout: 5 * time.Second}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		res.add("fixture", false, "%v", err)
		return res
	}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	srvURL := "http://" + ln.Addr().String()

	binDir := filepath.Join(env.RunDir, "fakebin")
	evDir := filepath.Join(env.RunDir, "evidence")
	if err := writeFakeCLIsMulti(binDir, evDir); err != nil {
		res.add("fake_cli", false, "%v", err)
		return res
	}
	evFile := filepath.Join(evDir, "s5-calls.txt")

	cfgPath := filepath.Join(env.RunDir, "config.json")
	cfg := fmt.Sprintf(`{
  "server": %q, "poll_interval_sec": 1, "timeout_sec": 30,
  "agents": [{"address":%q,"password":"x","cli":"opencode","workdir":%q}]
}`, srvURL, acctA, filepath.Join(env.RunDir, "wd-alpha"))
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
	logBuf := &strings.Builder{}
	cmd.Stdout, cmd.Stderr = logBuf, logBuf

	_ = env.TL.Add("note", "s5 start (12 backlog mails)", nil)
	if err := cmd.Start(); err != nil {
		res.add("spawn", false, "%v", err)
		return res
	}

	// 候证据：首轮唤醒落盘即收（积压轮一次即可断言）。
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(evFile); err == nil && strings.Contains(string(b), "12 未读") {
			break
		}
		select {
		case <-ctx.Done():
			res.add("budget", false, "budget hit waiting for batch evidence")
			_ = cmd.Process.Kill()
			return res
		case <-time.After(100 * time.Millisecond):
		}
	}
	time.Sleep(300 * time.Millisecond)
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()

	ev, err := os.ReadFile(evFile)
	if err != nil {
		res.add("evidence_read", false, "fake CLI never invoked (log: %s)", truncate(logBuf.String(), 300))
		return res
	}
	payload := string(ev)
	_ = env.TL.Add("evidence", "s5 digest payload", map[string]int{"bytes": len(payload)})

	// ① 计数行 + 截断提示
	res.add("count_line", strings.Contains(payload, "收件箱当前状态：12 未读"), "count line says 12 unread")
	res.add("truncation_note", strings.Contains(payload, "仅列最新 10 封") && strings.Contains(payload, "其余 2 封更早未读未列出"),
		"truncation note present (10 shown, 2 hidden)")

	// ② 完整性：S03…S12 在（最新 10 封），S01/S02 不在（被截断的是最旧两封）
	for i := 3; i <= 12; i++ {
		if !strings.Contains(payload, fmt.Sprintf("S%02d", i)) {
			res.add("completeness", false, "S%02d missing from the latest-10 payload", i)
			return res
		}
	}
	res.add("completeness", true, "S03..S12 all present in the payload")
	res.add("oldest_truncated", !strings.Contains(payload, "S01") && !strings.Contains(payload, "S02"),
		"the two oldest (S01/S02) are correctly hidden by the cap")

	// ③ 顺序：第一个条目主题=S12（最新），最后一个条目主题=S03（第 10 新）
	firstIdx := strings.Index(payload, "[未读 1/12]")
	lastIdx := strings.Index(payload, "[未读 10/12]")
	if firstIdx < 0 || lastIdx < 0 {
		res.add("numbering", false, "[未读 1/12] or [未读 10/12] markers missing")
		return res
	}
	res.add("numbering", true, "1/12..10/12 markers present")
	res.add("order_newest_first", strings.Contains(payload[firstIdx:lastIdx], "S12"),
		"first listed entry is the newest (S12)")
	res.add("order_tenth", strings.Contains(payload[lastIdx:], "S03"),
		"tenth listed entry is the 10th newest (S03)")
	return res
}

func init() { register(s5batch{}) }
