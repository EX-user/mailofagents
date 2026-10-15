package worker

import (
	"os"
	"testing"
)

// Test leg (alice 2026-10-15 15:39): reader vs codex's own booked TokenUsage.
// Runs only when CODEX_LEG_HOME + CODEX_LEG_THREAD are set (real codex run
// first); asserts our anchor+tail readout equals codex's books (tail=0 on a
// settled session).
func TestCodexCtxAgainstRealRollout(t *testing.T) {
	home := os.Getenv("CODEX_LEG_HOME")
	thread := os.Getenv("CODEX_LEG_THREAD")
	if home == "" || thread == "" {
		t.Skip("real-run leg: set CODEX_LEG_HOME + CODEX_LEG_THREAD")
	}
	env := map[string]string{"CODEX_HOME": home}
	got := codexSessionCtx(env, thread)
	const want = 8900 // codex books input 8890 + output 10
	if got != want {
		t.Fatalf("reader = %d, codex books = %d", got, want)
	}
}
