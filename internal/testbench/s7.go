package testbench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// s7 — 紧急打断 + 双旗标红线（M3 特征组合收官刀）：
//
// A. 紧急打断：emergency 配置（联络面+短语），慢 CLI（12s）拖过
//    watchUrgent 的 10s 节拍；唤醒中途台架注入紧急信（联络面发件人+
//    短语主题）→ 打断入账 → 重唤醒 digest 紧急信置顶（最新优先）。
// B. -compact 红线：对已绑定会话的账户跑 -compact（codex 线，无
//    Compacter 入口）→ 断言「不进会话生成」：假 CLI 零调用，日志
//    「compact: session kept」，进程 exit 0。
//
// 0903 晨实况（线上抖动致 poll 失败、恢复后自动追赶）即本剧本原型——
// 此条为 S4 注；本刀红线注：-compact 永不进会话生成（三轮定稿语义）。

type s7combo struct{}

func (s7combo) Name() string { return "s7-urgent-and-compact-line" }
func (s7combo) Desc() string {
	return "s7 紧急打断 e2e（打断入账/重醒置顶）+ -compact 红线（零会话生成）"
}
func (s7combo) Timeout() time.Duration { return 3 * time.Minute }

const urgentSubject = "[紧急] 生产故障需要立即处理"

// mailStore is a mutable fixture mailbox: the bench can inject mail
// mid-run (the urgent interrupt needs mail ARRIVING during a wake).
type mailStore struct {
	mu    sync.Mutex
	mails []MailSummary
}

func (m *mailStore) append(mail MailSummary) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mails = append(m.mails, mail)
}

func (m *mailStore) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		switch r.URL.Path {
		case "/api/inbox":
			// 契约保真：生产 /api/inbox 最新在前（Digest 不排序，直接按
			// 返回序渲染「新→旧」），假信箱必须同样降序，否则摘要序失真。
			sorted := make([]MailSummary, len(m.mails))
			copy(sorted, m.mails)
			sort.SliceStable(sorted, func(i, j int) bool {
				return sorted[i].ReceivedAt > sorted[j].ReceivedAt
			})
			_ = jsonEncodeMap(w, map[string]any{
				"messages": sorted, "count": len(sorted),
				"total_count": len(sorted), "unread_count": len(sorted),
			})
		case "/api/stats":
			_ = jsonEncodeMap(w, map[string]any{"inbox_total": len(m.mails), "unread": len(m.mails), "sent_total": 0})
		case "/api/subs":
			_ = jsonEncodeMap(w, map[string]any{"subordinates": nil, "superiors": nil})
		default:
			http.NotFound(w, r)
		}
	}
}

func (s s7combo) Run(ctx context.Context, env *Env) Result {
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

	// ---- A. 紧急打断 ----
	store := &mailStore{mails: []MailSummary{{
		ID: "01FIXTURE0000000000000000X", From: "actor@fixture.test",
		Subject: fixtureSubject, Preview: fixturePreview,
		Unread: true, ReceivedAt: time.Now().Unix(),
	}}}
	mux := http.NewServeMux()
	mux.Handle("/api/", store.handler())
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
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
	evFile := filepath.Join(evDir, "s7-calls.txt")

	cfgPath := filepath.Join(env.RunDir, "config.json")
	wdAlpha := filepath.Join(env.RunDir, "wd-alpha")
	if err := os.MkdirAll(wdAlpha, 0o755); err != nil {
		res.add("workdir", false, "%v", err)
		return res
	}
	cfg := fmt.Sprintf(`{
  "server": %q, "poll_interval_sec": 1, "timeout_sec": 30,
  "emergency": {"addresses": ["actor@fixture.test"], "urgent_phrase": "[紧急] 生产故障"},
  "agents": [{"address":%q,"password":"x","cli":"opencode","workdir":%q}]
}`, srvURL, acctA, wdAlpha)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		res.add("config", false, "%v", err)
		return res
	}

	cmd := exec.Command(env.WorkerBin, "-config", cfgPath)
	cmd.Env = WhitelistEnv(root,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"EV="+evFile,
		"BENCH_SLOW=12", // 拖过 watchUrgent 的 10s 节拍
	)
	cmd.Dir = env.RunDir
	logBuf := &bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = logBuf, logBuf

	_ = env.TL.Add("note", "s7 phase A start (slow CLI 12s, urgent injection at ~2s)", nil)
	if err := cmd.Start(); err != nil {
		res.add("spawn", false, "%v", err)
		return res
	}
	// 收尸协程：进程早退时立即 reap。不收尸的话 Signal(0) 对僵尸返回 nil，
	// 下面的 alive 探活会误报「存活」。
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	// 唤醒已开始（证据首块落盘）后注入紧急信。假 CLI 的 argv 不含 workdir，
	// 所以等「=== CALL ===」标记而不是路径串。
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(evFile); err == nil && strings.Contains(string(b), "=== CALL ===") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(1 * time.Second)
	store.append(MailSummary{
		ID: "01FIXTUREURGENT00000000000X", From: "actor@fixture.test",
		Subject: urgentSubject, Preview: "值守中断令：立即优先处理。",
		Unread: true, ReceivedAt: time.Now().Unix(),
	})
	_ = env.TL.Add("note", "urgent mail injected mid-wake", nil)

	// 候打断+重唤醒：证据出现紧急主题。
	urgentSeen := false
	deadline = time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) && !urgentSeen {
		if b, err := os.ReadFile(evFile); err == nil {
			urgentSeen = strings.Contains(string(b), urgentSubject)
		}
		select {
		case <-ctx.Done():
		case <-time.After(200 * time.Millisecond):
		}
	}
	alive := false
	select {
	case <-waitCh:
		// 中途退出（含 load config 失败的 log.Fatalf）
	default:
		alive = true
		_ = cmd.Process.Kill()
		<-waitCh
	}
	log := logBuf.String()
	// 日志全文落盘（回放包档案面：时间线只记指针）。
	logPath := filepath.Join(env.RunDir, "s7-worker-a.log")
	_ = os.WriteFile(logPath, []byte(log), 0o644)
	_ = env.TL.Add("evidence", "s7 worker log", map[string]any{"bytes": len(log), "path": logPath})

	res.add("interrupt_logged", strings.Contains(log, "urgent interrupt: wake cancelled, re-waking with urgent mail"),
		"log carries the urgent-interrupt line")
	res.add("urgent_delivered", urgentSeen, "re-wake digest carries the urgent mail")
	if b, err := os.ReadFile(evFile); err == nil {
		blocks := strings.Split(string(b), "=== CALL ===")
		var pre, post string
		for _, bl := range blocks {
			if !strings.Contains(bl, "wd-alpha") {
				continue
			}
			if !strings.Contains(bl, urgentSubject) {
				pre = bl
			} else if post == "" {
				post = bl
			}
		}
		res.add("first_wake_without_urgent", pre != "", "a pre-injection wake exists")
		// 紧急信置顶：digest 中紧急主题出现在普通主题之前（新→旧）。
		res.add("urgent_first_in_digest", post != "" && strings.Index(post, urgentSubject) < strings.Index(post, fixtureSubject),
			"urgent mail listed before the normal mail in the re-wake digest")
	}
	res.add("worker_survives_A", alive, "worker alive through interrupt+re-wake")

	// ---- B. -compact 红线（codex 线：无 Compacter 入口）----
	cfg2 := fmt.Sprintf(`{
  "server": %q, "poll_interval_sec": 1, "timeout_sec": 30,
  "agents": [{"address":%q,"password":"x","cli":"codex","workdir":%q}]
}`, srvURL, acctA, filepath.Join(env.RunDir, "wd-alpha"))
	cfgB := filepath.Join(env.RunDir, "config-compact.json")
	if err := os.WriteFile(cfgB, []byte(cfg2), 0o644); err != nil {
		res.add("config_B", false, "%v", err)
		return res
	}
	// 预置绑定 state（-compact 对空绑定是安全空转，红线断言要有真绑定）。
	statePath := filepath.Join(env.RunDir, "config-compact.alpha.state.json")
	if err := os.WriteFile(statePath, []byte(fmt.Sprintf(`{"session_id":%q}`, sess)), 0o600); err != nil {
		res.add("seed_state", false, "%v", err)
		return res
	}
	callsBefore := evidenceCallCount(evFile)

	compact := exec.Command(env.WorkerBin, "-config", cfgB, "-compact", "alpha")
	compact.Env = WhitelistEnv(root,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"EV="+evFile,
	)
	compact.Dir = env.RunDir
	cBuf := &bytes.Buffer{}
	compact.Stdout, compact.Stderr = cBuf, cBuf
	runErr := compact.Run()

	res.add("compact_exit_zero", runErr == nil, "exit err=%v, output: %s", runErr, truncate(cBuf.String(), 160))
	// 红线日志（codex 无 Compacter 入口的实装措辞，duty.compactOnce）:
	// 「compact: cli=codex has no headless compact entry — session kept …」
	compactLog := cBuf.String()
	_ = os.WriteFile(filepath.Join(env.RunDir, "s7-worker-b.log"), []byte(compactLog), 0o644)
	res.add("compact_session_kept_logged", strings.Contains(compactLog, "no headless compact entry") && strings.Contains(compactLog, "session kept"),
		"log carries the no-entry/session-kept line (codex has no Compacter entry), got: %s", truncate(compactLog, 160))
	callsAfter := evidenceCallCount(evFile)
	res.add("compact_zero_session_spawn", callsAfter == callsBefore,
		"fake CLI calls before/after -compact: %d/%d (red line: never enters session generation)", callsBefore, callsAfter)
	return res
}

func evidenceCallCount(evFile string) int {
	b, err := os.ReadFile(evFile)
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "=== CALL ===")
}

func jsonEncodeMap(w http.ResponseWriter, v any) error {
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(v)
}

func init() { register(s7combo{}) }
