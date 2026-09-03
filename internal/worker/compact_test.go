package worker

import (
	"context"
	"testing"
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
