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
	d.saveState()

	d2 := NewDuty(cfg, false, false)
	d2.loadState()
	if d2.sessionID != "sess-old" || d2.pushCounts["01MAIL"] != 5 {
		t.Fatalf("round trip: session=%q counts=%v", d2.sessionID, d2.pushCounts)
	}
}
