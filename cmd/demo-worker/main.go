// demo-worker — the zero-config TUI experience pack (boss request 2026-09-22):
// run it and the status board comes up with mock data, no config file, no
// server. The rendering and interaction code is THE worker's own (same
// package), only the data source is an inbuilt carousel:
//
//	go run ./cmd/demo-worker        (or go install; then run `demo-worker`)
//
// Experience points covered: SIGWINCH full repaint (resize the terminal),
// mouse buttons per row (停止 interrupts the mock "wake", 压缩 forces the
// compact state, 复制 copies a fake session id via OSC52), and the five
// states carousel (working/waiting/compact/ARMING/error).
package main

import (
	"context"
	"fmt"
	"math/rand"
	"os/signal"
	"syscall"
	"time"

	worker "github.com/agentmail/agentmail/internal/worker"
)

type boardAction = worker.BoardAction

var states = []string{"working", "waiting", "compact", "arming", "error"}

type demoAccount struct {
	Tag      string
	state    string
	details  map[string]string
	session  string
	ctxPct   int
}

func main() {
	fmt.Println("demo worker — mock TUI, no config, no server (Ctrl-C to quit)")
	worker.SetMeta("demo-worker", "this is a demo: all data is mock, nothing is real")
	worker.SetMouse(true) // the whole point: buttons live without any config

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	accts := []demoAccount{
		{Tag: "alpha", details: map[string]string{
			"working": "digest sent, model is on it…",
			"waiting": "0 unread",
			"compact": "compacting session in place…",
			"arming":  "digest sent, awaiting first output…",
			"error":   "wake failed: provider quota/429 · insufficient_balance",
		}},
		{Tag: "bravo", details: map[string]string{
			"working": "step_start · reading internal/worker/duty.go",
			"waiting": "2 unread",
			"compact": "compacting session in place…",
			"arming":  "digest sent, awaiting first output…",
			"error":   "wake failed: connection refused (backoff 2/5)",
		}},
		{Tag: "charlie", details: map[string]string{
			"working": "tool · bash · go test ./...",
			"waiting": "1 unread",
			"compact": "compacting session in place…",
			"arming":  "digest sent, awaiting first output…",
			"error":   "wake failed: provider quota/429 · rate limited",
		}},
	}
	start := time.Now()
	for i := range accts {
		a := &accts[i]
		a.state = states[i%len(states)]
		a.session = fmt.Sprintf("01DEMO%s0123456789ABCDE", a.Tag[:1])
		worker.AddRow(a.Tag, start, 200000, 150000)
		worker.Set(a.Tag, a.state, a.details[a.state])
		worker.SetCtx(a.Tag, int64(30000+i*45000))
	}

	// actions: the same buttons a real operator would click
	go func() {
		for _, tag := range []string{"alpha", "bravo", "charlie"} {
			ch := worker.SubscribeActions(tag)
			go func(tag string, ch <-chan boardAction) {
				for a := range ch {
					switch a.Kind {
					case "stop":
						worker.Set(tag, "waiting", "stopped by demo operator · mail re-queued")
						worker.Logf(tag, "board: wake stopped by operator (mock)")
					case "compact":
						worker.Set(tag, "compact", "compacting session in place…")
						worker.Logf(tag, "board: compact requested (mock)")
					case "copy":
						worker.Logf(tag, "board: session id copied to clipboard (mock)")
					}
				}
			}(tag, ch)
		}
	}()

	// input diagnostics (remote debugging): the worker-log line reports
	// how many raw input events arrived, so a screenshot tells whether the
	// terminal's event channel is live
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				recs, keys, mouse, mode, errText := worker.WinDiag()
				if recs < 0 {
					worker.Logf("input", "raw input events so far: %d", worker.InputCount())
				} else {
					worker.Logf("input", "recs=%d keys=%d mouse=%d mode=0x%x err=%q | raw=%d",
						recs, keys, mouse, mode, errText, worker.InputCount())
				}
			}
		}
	}()

	// the carousel: every few seconds one account advances its state and
	// rolls a fake stream line, so the board keeps living
	go func() {
		tick := time.NewTicker(3 * time.Second)
		defer tick.Stop()
		step := 0
		rolls := map[string][]string{
			"alpha":   {"step_start · reading internal/worker/duty.go", "tool · bash · go test ./internal/worker/"},
			"bravo":   {"thinking…", "step_start · drafting reply digest"},
			"charlie": {"step_start · exploring repo layout"},
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				a := &accts[step%len(accts)]
				step++
				next := states[(rand.Intn(len(states)))]
				a.state = next
				worker.Set(a.Tag, next, a.details[next])
				worker.SetCtx(a.Tag, int64(20000+rand.Intn(160000)))
				line := rolls[a.Tag][rand.Intn(len(rolls[a.Tag]))]
				worker.Set(a.Tag, "", fmt.Sprintf("%s · +%d tok", line, 100+rand.Intn(900)))
			}
		}
	}()

	worker.RenderLoop(ctx)
}
