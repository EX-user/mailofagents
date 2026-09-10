package worker

import (
	"context"
	"testing"
	"time"
)

func testContext() context.Context { return context.Background() }

// -compact / -compact-before-wake share compactOnce: an account with no
// bound session is a safe no-op on every adapter (nothing to compress, no
// generation, no error). Adapters WITH a headless entry take the Compacter
// path only when a session exists (live behavior verified against real
// sessions; here we pin the no-op contract).
func TestCompactOnceNoSessionNoop(t *testing.T) {
	for _, id := range []string{"pi", "opencode", "claude", "codex"} {
		cfg := &Config{Address: "t@e.com", CLI: id, Workdir: t.TempDir()}
		d := NewDuty(cfg, false, false)
		d.loadState()
		if err := d.compactOnce(testContext()); err != nil {
			t.Errorf("%s: compactOnce on empty binding = %v, want nil", id, err)
		}
	}
}

// NewDuty carries the compact-before-wake marker into the duty struct.
func TestNewDutyCompactBeforeWakeMarker(t *testing.T) {
	cfg := &Config{Address: "t@e.com", CLI: "pi", Workdir: t.TempDir()}
	if NewDuty(cfg, false, false).compactBeforeWake {
		t.Error("marker set without opt-in")
	}
	if !NewDuty(cfg, false, true).compactBeforeWake {
		t.Error("marker lost")
	}
}

// boss report 0911: -compact-before-wake logged the compression but the
// board row never left WAITING; the in-loop path showed WORKING instead of
// COMPACT. Contract now: COMPACT shows only while a compression is in
// flight, and compactOnce must NEVER leave a row stuck in COMPACT after
// it returns (any path: no-op, failure, success).
func TestCompactOnceNeverSticksCompactState(t *testing.T) {
	board.AddRow("cptest", time.Now(), 0, 0)
	t.Cleanup(func() { board.rows = nil })

	// no-op path (empty binding): state must stay as-is, never COMPACT
	d := NewDuty(&Config{Address: "cptest@e.com", CLI: "opencode", Workdir: t.TempDir()}, false, false)
	d.loadState()
	if err := d.compactOnce(testContext()); err != nil {
		t.Fatalf("empty-binding no-op errored: %v", err)
	}
	if row := board.row("cptest"); row != nil && row.state == "compact" {
		t.Error("COMPACT set with nothing to compress")
	}

	// failure path (Compacter adapter, no reachable server): must restore
	// a resting state — COMPACT only exists mid-compression
	d.sessionID = "01TESTSESSION0000000000000000"
	if err := d.compactOnce(testContext()); err == nil {
		t.Log("compact unexpectedly succeeded (no server expected here)")
	}
	if row := board.row("cptest"); row != nil && row.state == "compact" {
		t.Error("row stuck in COMPACT after compactOnce returned")
	}
}
