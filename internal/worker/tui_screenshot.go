package worker

import (
	"fmt"
	"time"
)

// DumpTUIScreenshots prints synthetic TUI frames to stdout — the bench
// acceptance artifacts for the v0.2.8 TUI upgrade (boss: "不跑 worker 直接
// 看到效果"). Covers the four states, the two-line rolling area, the
// worker-log pane with its hint line, and long-line truncation.
func DumpTUIScreenshots(width int, version string) {
	if width < 40 {
		width = 40
	}
	started := time.Now().Add(-15*time.Minute - 17*time.Second)
	since := time.Now().Add(-40 * time.Second)

	longDetail := "wake failed: provider quota/429 · insufficient_balance: 您的余额不足请充值后再试 (this line is deliberately long to prove truncation at the frame width)"
	longRoll := "step_start · bash · ls -la /very/long/path/that/goes/on/and/on/and/on/until/it/surely/passes/the/frame/width/and/gets/clamped"

	mkrows := func() []*statusRow {
		return []*statusRow{
			{tag: "alpha", state: "working", detail: "digest sent, model is on it…",
				since: since, started: started, ctxTokens: 196000, ctxWindow: 1000000},
			{tag: "bravo", state: "waiting", detail: "2 unread",
				since: since, started: started, ctxTokens: 97300, ctxWindow: 1000000},
			{tag: "charlie", state: "compact", detail: "compacting session in place…",
				since: since, started: started, ctxTokens: 812000, ctxWindow: 1000000},
			{tag: "delta", state: "error", detail: longDetail,
				since: since, started: started},
		}
	}
	hint := "/var/lib/agentmail/worker/errors-*.log + WORKER_LOG_FILE (stdout mirror)"
	ring := []string{
		"[alpha] wake failed: provider quota/429 · session salvaged",
		"[bravo] last ok: 01JABCDEF0123456789ABCDEF",
		"[alpha] poll failed: connection refused (backoff 2/5)",
	}

	fmt.Printf("=== frame 1: four states (working/waiting/compact/error) @ %d cols ===\n", width)
	fmt.Println(renderFrame(width, time.Date(2026, 9, 8, 9, 46, 2, 0, time.Local), version,
		mkrows(), map[string][]string{
			"alpha": {"step_start · reading internal/worker/duty.go", "tool · bash · go test ./internal/worker/"},
			"bravo": {"digest: [addr] 2 封未读（新→旧）…"},
		}, ring, hint))

	fmt.Printf("\n=== frame 2: long-line truncation (CJK wide runes + ASCII, %d cols) ===\n", width)
	longRows := []*statusRow{
		{tag: "alpha", state: "error", detail: longDetail, since: since, started: started},
	}
	fmt.Println(renderFrame(width, time.Date(2026, 9, 8, 9, 46, 2, 0, time.Local), version,
		longRows, map[string][]string{"alpha": {longRoll, "tool · edit · config.go [行内容同样超长以验证滚动区截断边界]"}},
		[]string{"[alpha] " + longDetail}, hint))

	fmt.Printf("\n=== frame 3: empty board (bootstrap, no accounts yet) ===\n")
	fmt.Println(renderFrame(width, time.Date(2026, 9, 8, 9, 46, 2, 0, time.Local), version,
		nil, map[string][]string{}, nil, hint))
}
