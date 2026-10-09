package worker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPushCountsStateRoundTrip(t *testing.T) {
	// boss spec 2026-10-01: push counts must survive restarts (a session
	// that forgets to clear unread does not get forgiven by a worker
	// restart). Old state files (session_id only) must keep loading.
	dir := t.TempDir()
	cfg := &Config{StateFile: filepath.Join(dir, "state.json")}
	d := NewDuty(cfg, false, false)
	if err := os.WriteFile(cfg.StateFile, []byte(`{"session_id":"sess-old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d.loadState()
	if d.sessionID != "sess-old" {
		t.Fatalf("legacy state: session id = %q", d.sessionID)
	}
	if d.pushCounts == nil || len(d.pushCounts) != 0 {
		t.Fatalf("legacy state: push counts not initialized: %v", d.pushCounts)
	}

	d.pushCounts["01MAIL"] = 5
	d.lastCtx = 39000
	d.lastErr = "wake failed: boom"
	d.saveState()

	d2 := NewDuty(cfg, false, false)
	d2.loadState()
	if d2.sessionID != "sess-old" || d2.pushCounts["01MAIL"] != 5 {
		t.Fatalf("round trip: session=%q counts=%v", d2.sessionID, d2.pushCounts)
	}
	if d2.lastCtx != 39000 {
		t.Fatalf("round trip: last_ctx = %d", d2.lastCtx)
	}
	if d2.lastErr != "wake failed: boom" {
		t.Fatalf("round trip: last_err = %q", d2.lastErr)
	}
}

func TestWakeInFlightResumeFlag(t *testing.T) {
	// 恢复现场 ③b (alice ruling + three guards): the in-flight marker
	// persists across restarts, and the resume consumes it once — guard 1
	// (一次性): a resume that dies again takes the normal path.
	dir := t.TempDir()
	cfg := &Config{StateFile: filepath.Join(dir, "state.json")}
	d := NewDuty(cfg, false, false)
	if err := os.WriteFile(cfg.StateFile,
		[]byte(`{"session_id":"s1","wake_in_flight":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d.loadState()
	if !d.wakeInFlight || d.sessionID != "s1" {
		t.Fatalf("load: inFlight=%v session=%q", d.wakeInFlight, d.sessionID)
	}

	// clear + persist (what Run does before resumeWake)
	d.mu.Lock()
	d.wakeInFlight = false
	d.saveState()
	d.mu.Unlock()

	d2 := NewDuty(cfg, false, false)
	d2.loadState()
	if d2.wakeInFlight {
		t.Fatal("guard 1 violated: flag survived the clear")
	}
}

func TestMouseTriStateDefault(t *testing.T) {
	// boss 2026-10-09: absent `mouse` = OFF (final ruling, reversing the
	// 10-03 flip before it ever shipped); explicit true turns it on.
	if MouseOn(nil) {
		t.Fatal("absent mouse must default OFF")
	}
	f := false
	if MouseOn(&f) {
		t.Fatal("explicit false must stay off")
	}
	tr := true
	if !MouseOn(&tr) {
		t.Fatal("explicit true must stay on")
	}
}

func TestMouseAbsentMeansOff(t *testing.T) {
	// boss 2026-10-09 final: absent = OFF (the 10-03 flip reversed before
	// it shipped); true opts in.
	if MouseOn(nil) {
		t.Fatal("absent mouse must default OFF")
	}
	tr := true
	if !MouseOn(&tr) {
		t.Fatal("explicit true must enable")
	}
	f := false
	if MouseOn(&f) {
		t.Fatal("explicit false must stay off")
	}
}
