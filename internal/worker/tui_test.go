package worker

import (
	"strings"
	"testing"
	"time"
)

func TestRenderFrameStatesAndPanes(t *testing.T) {
	started := time.Now().Add(-15 * time.Minute)
	rows := []*statusRow{
		{tag: "alpha", state: "working", detail: "digest sent, model is on it…", started: started, ctxTokens: 196000, ctxWindow: 1000000},
		{tag: "bravo", state: "error", detail: "wake failed: quota", started: started},
	}
	rolls := map[string][]string{"alpha": {"roll old", "roll new"}}
	ring := []string{"[alpha] wake failed: quota", "[bravo] last ok"}
	frame := renderFrame(100, time.Date(2026, 9, 8, 9, 46, 0, 0, time.Local), "v0.2.8-test", rows, rolls, ring, "errors-*.log")

	for _, want := range []string{
		"version: v0.2.8-test", // header
		"[alpha] WORKING · up", // row line (uppercase state + separator)
		"roll old", "roll new", // two-line rolling area
		"[bravo] ERROR",           // error state renders
		"[worker-log]",            // log pane header
		"full logs: errors-*.log", // hint line
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame missing %q:\n%s", want, frame)
		}
	}
	// rolling area: latest last
	if strings.Index(frame, "roll new") < strings.Index(frame, "roll old") {
		t.Error("rolling area must be oldest-first")
	}
	// every line fits the width (no wrapping)
	for _, line := range strings.Split(frame, "\n") {
		if cols := visualCols(line); cols > 100 {
			t.Errorf("line exceeds 100 cols (%d): %q", cols, line)
		}
	}
}

func TestRenderFrameRollCap(t *testing.T) {
	rolls := map[string][]string{"a": {"1", "2", "3", "4", "5"}}
	frame := renderFrame(100, time.Now(), "v", []*statusRow{{tag: "a", state: "working", started: time.Now()}}, rolls, nil, "")
	if strings.Contains(frame, "\n    1\n") || strings.Contains(frame, "\n    2\n") {
		t.Error("roll area must cap at the newest 2 lines")
	}
	if !strings.Contains(frame, "    4") || !strings.Contains(frame, "    5") {
		t.Error("roll area must keep the newest lines")
	}
}

func TestRenderFrameLogRingCap(t *testing.T) {
	ring := []string{"l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8", "l9", "l10", "l11", "l12"}
	frame := renderFrame(100, time.Now(), "v", nil, map[string][]string{}, ring, "")
	for _, dead := range []string{"    l1\n", "    l2\n"} {
		if strings.Contains(frame, dead) {
			t.Errorf("log ring must drop oldest: %s found", dead)
		}
	}
	if !strings.Contains(frame, "l12") || !strings.Contains(frame, "l10") {
		t.Error("log ring must keep the newest 10")
	}
}

// boss 0910 spec + correction: rolling area wraps to two right-indented
// rows with a trailing "…" hard cut; worker-log lines do NOT wrap — one
// indented line each, hard cut with trailing "…".
func TestRenderFrameIndentAndCut(t *testing.T) {
	long := strings.Repeat("word ", 30) // 150 cols — must cut, not wrap
	ring := []string{long}
	rolls := map[string][]string{"a": {"<event content that is quite long and keeps going past the pane width for wrapping>"}}
	frame := renderFrame(100, time.Now(), "v",
		[]*statusRow{{tag: "a", state: "working", started: time.Now()}}, rolls, ring, "errors-*.log")
	if strings.Contains(frame, "  | ") {
		t.Errorf("gutter glyph must be gone (right-indent form):\n%s", frame)
	}
	if !strings.Contains(frame, "…") {
		t.Errorf("over-width content must carry a trailing …:\n%s", frame)
	}
	// the log line must be a single row (no wrap): exactly one indented row starts with "word"
	if n := strings.Count(frame, "\n    word "); n != 1 {
		t.Errorf("log line must not wrap (want 1 row, got %d):\n%s", n, frame)
	}
	for _, line := range strings.Split(frame, "\n") {
		if cols := visualCols(line); cols > 100 {
			t.Errorf("line exceeds 100 cols (%d): %q", cols, line)
		}
	}
}

// visualCols counts display columns (wide runes = 2), mirroring clampCols.
func visualCols(s string) int {
	n := 0
	for _, r := range s {
		n += runeWidth(r)
	}
	return n
}
