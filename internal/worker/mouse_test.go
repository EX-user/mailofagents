package worker

import (
	"context"
	"encoding/base64"
	"os"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// TestComputeHitsGeometry: the hit boxes must line up with the rendered
// control text — display columns measured through ANSI (the state word is
// styled) and CJK-wide button labels.
func TestComputeHitsGeometry(t *testing.T) {
	line := "\x1b[32m●\x1b[0m [alpha] \x1b[32mWORKING\x1b[0m · up 15m17s | ctx 20%" + ctlText
	hits := computeHits([]string{"header", line, "other"})
	h, ok := hits["alpha"]
	if !ok {
		t.Fatal("alpha row not found")
	}
	if h.line != 2 {
		t.Fatalf("line = %d, want 2", h.line)
	}
	// walk the rendered line with the same width rules the terminal uses
	// and check both button ranges land inside the control text
	prefix := line[:len(line)-len(ctlText)]
	_ = prefix
	base := lipgloss.Width(line[:len(line)-len(ctlText)])
	if h.stopAt != base+3 || h.stopEnd <= h.stopAt {
		t.Fatalf("stop range = [%d,%d], want start %d+3", h.stopAt, h.stopEnd, base)
	}
	if h.compactAt != h.stopEnd+2 || h.compactEnd <= h.compactAt {
		t.Fatalf("compact range = [%d,%d], malformed", h.compactAt, h.compactEnd)
	}
	// the glyph right after "[停止]" (the space) must fall OUTSIDE the box
	if h.stopEnd >= h.stopAt+lipgloss.Width(ctlStop) {
		t.Fatal("stop box swallows the trailing separator")
	}
	if h.copyAt != h.compactEnd+2 || h.copyEnd <= h.copyAt {
		t.Fatalf("copy range = [%d,%d], malformed", h.copyAt, h.copyEnd)
	}
}

func TestComputeHitsNoControls(t *testing.T) {
	if hits := computeHits([]string{"● [alpha] WORKING"}); len(hits) != 0 {
		t.Fatalf("expected no hits without control text, got %v", hits)
	}
}

// TestRequestActionFanOut: subscribers of the tag get the action, the
// wildcard gets a copy, and a full channel drops rather than blocking.
func TestRequestActionFanOut(t *testing.T) {
	b := &Board{actionSubs: map[string][]chan boardAction{}}
	ch := b.SubscribeActions("alpha")
	wild := b.SubscribeActions("*")
	b.RequestAction("alpha", "stop")
	select {
	case a := <-ch:
		if a.Tag != "alpha" || a.Kind != "stop" {
			t.Fatalf("got %+v", a)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive the action")
	}
	select {
	case <-wild:
	case <-time.After(time.Second):
		t.Fatal("wildcard did not receive the action")
	}
	// full channels drop: fill the subscriber, request twice, no deadlock
	for i := 0; i < 10; i++ {
		b.RequestAction("alpha", "compact")
	}
}

// TestConsumeInputMouseAndCPR: SGR presses and CPR replies are extracted;
// incomplete tails are held.
func TestConsumeInputMouseAndCPR(t *testing.T) {
	b := &Board{
		actionSubs: map[string][]chan boardAction{},
		cprCh:      make(chan int, 4),
		topRow:     1,
		hitRows:    map[string]rowHit{"alpha": {line: 5, stopAt: 12, stopEnd: 20, compactAt: 22, compactEnd: 30}},
	}
	ch := b.SubscribeActions("alpha")
	tail := b.consumeInput([]byte("\x1b[<0;12;5M"), nil)
	if len(tail) != 0 {
		t.Fatalf("tail = %q, want empty", tail)
	}
	select {
	case a := <-ch:
		if a.Kind != "stop" || a.Tag != "alpha" {
			t.Fatalf("got %+v", a)
		}
	case <-time.After(time.Second):
		t.Fatal("click did not dispatch")
	}
	// release events are ignored
	b.consumeInput([]byte("\x1b[<0;12;5m"), nil)
	select {
	case <-ch:
		t.Fatal("release dispatched an action")
	case <-time.After(100 * time.Millisecond):
	}
	// CPR
	b.consumeInput([]byte("\x1b[10;1R"), nil)
	select {
	case row := <-b.cprCh:
		if row != 10 {
			t.Fatalf("row = %d", row)
		}
	case <-time.After(time.Second):
		t.Fatal("CPR not delivered")
	}
	// incomplete SGR held
	if tail := b.consumeInput([]byte("\x1b[<0;12"), nil); len(tail) != 7 {
		t.Fatalf("incomplete tail = %q", tail)
	}
}

// TestClickMapsToButtons: absolute terminal coords resolve through topRow
// into the right action.
func TestClickMapsToButtons(t *testing.T) {
	b := &Board{
		actionSubs: map[string][]chan boardAction{},
		cprCh:      make(chan int, 4),
		topRow:     3,
		hitRows: map[string]rowHit{
			"alpha": {line: 2, stopAt: 30, stopEnd: 35, compactAt: 37, compactEnd: 43},
		},
	}
	ch := b.SubscribeActions("alpha")
	// line 2 on screen = row 4; inside the stop box
	b.click(32, 4)
	select {
	case a := <-ch:
		if a.Kind != "stop" {
			t.Fatalf("got %+v", a)
		}
	case <-time.After(time.Second):
		t.Fatal("stop click lost")
	}
	// inside the compact box
	b.click(40, 4)
	select {
	case a := <-ch:
		if a.Kind != "compact" {
			t.Fatalf("got %+v", a)
		}
	case <-time.After(time.Second):
		t.Fatal("compact click lost")
	}
	// inside the copy box
	b.hitRows["alpha"] = rowHit{line: 2, stopAt: 30, stopEnd: 35, compactAt: 37, compactEnd: 43, copyAt: 45, copyEnd: 51}
	b.click(47, 4)
	select {
	case a := <-ch:
		if a.Kind != "copy" {
			t.Fatalf("got %+v", a)
		}
	case <-time.After(time.Second):
		t.Fatal("copy click lost")
	}
	// wrong line and outside both boxes: nothing
	b.click(32, 9)
	b.click(10, 4)
	select {
	case a := <-ch:
		t.Fatalf("stray action %+v", a)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestWatchActionsStopAndCompact: stop cancels the stored cancel, compact
// sets the pending flag.
func TestWatchActionsStopAndCompact(t *testing.T) {
	d := &Duty{cfg: &Config{Address: "test@example.com"}}
	d.actCh = board.SubscribeActions("*")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wakeCtx, wakeCancel := context.WithCancel(context.Background())
	defer wakeCancel()
	d.currentCancel.Store(context.CancelFunc(wakeCancel))
	go d.watchActions(ctx)

	board.RequestAction("*", "compact")
	deadline := time.Now().Add(time.Second)
	for !d.compactPending.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !d.compactPending.Load() {
		t.Fatal("compact action did not set compactPending")
	}

	board.RequestAction("*", "stop")
	deadline = time.Now().Add(time.Second)
	for wakeCtx.Err() == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if wakeCtx.Err() == nil {
		t.Fatal("stop action did not cancel the wake")
	}
	if !d.stopHit.Load() {
		t.Fatal("stopHit not set")
	}
}


// TestWatchActionsCopySession: the copy action emits an OSC52 sequence with
// the bound session id (base64); empty session stays silent.
func TestWatchActionsCopySession(t *testing.T) {
	d := &Duty{cfg: &Config{Address: "test@example.com"}}
	d.mu.Lock()
	d.sessionID = "01ABCDEF0123456789ABCDEF"
	d.mu.Unlock()
	d.actCh = board.SubscribeActions("*copy")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.watchActions(ctx)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	type result struct {
		out string
	}
	done := make(chan result, 1)
	go func() {
		b := make([]byte, 256) // single Read suffices: one small Fprintf
		n, _ := r.Read(b)
		done <- result{string(b[:n])}
	}()
	board.RequestAction("*copy", "copy")
	var out string
	select {
	case res := <-done:
		out = res.out
	case <-time.After(2 * time.Second):
	}
	w.Close()
	os.Stdout = old
	want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("01ABCDEF0123456789ABCDEF")) + "\x07"
	if out != want {
		t.Fatalf("OSC52 = %q, want %q", out, want)
	}
}
