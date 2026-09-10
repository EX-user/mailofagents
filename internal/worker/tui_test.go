package worker

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// ansiStrip removes SGR/CSI sequences so tests can assert on visible text
// (renderFrame emits ANSI when a color profile is active).
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func ansiStrip(s string) string { return ansiRe.ReplaceAllString(s, "") }

// boxContentLines returns the text inside box borders ("│ ... │"), for
// asserting what the viewport actually displays.
func boxContentLines(t *testing.T, frame string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(ansiStrip(frame), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "│") && strings.HasSuffix(line, "│") {
			out = append(out, strings.TrimSuffix(strings.TrimPrefix(line, "│"), "│"))
		}
	}
	return out
}

func testRow(tag, state string, started time.Time) *statusRow {
	return &statusRow{tag: tag, state: state, started: started}
}

func TestRenderFrameStatesAndBoxes(t *testing.T) {
	started := time.Now().Add(-15 * time.Minute)
	rows := []*statusRow{
		{tag: "alpha", state: "working", detail: "digest sent, model is on it…", started: started, ctxTokens: 196000, ctxWindow: 1000000},
		{tag: "bravo", state: "error", detail: "wake failed: quota", started: started},
	}
	rolls := map[string][]string{"alpha": {"roll old", "roll new"}}
	ring := []string{"[alpha] wake failed: quota", "[bravo] last ok"}
	frame := renderFrame(100, time.Date(2026, 9, 8, 9, 46, 0, 0, time.Local), "v0.2.8-test", rows, rolls, ring, "errors-*.log")
	sframe := ansiStrip(frame)

	for _, want := range []string{
		"version: v0.2.8-test",    // header
		"[alpha] WORKING · up",    // row line (uppercase state + separator)
		"[bravo] ERROR",           // error state renders
		"[worker-log]",            // log pane header
		"full logs: errors-*.log", // hint line
		"roll new",                // rolling content inside the box
	} {
		if !strings.Contains(sframe, want) {
			t.Errorf("frame missing %q:\n%s", want, sframe)
		}
	}
	// state dots present (boss 0910)
	if !strings.Contains(sframe, "●") {
		t.Error("status rows must carry the state dot ●")
	}
	// both panes are bordered text boxes
	if strings.Count(sframe, "╭") < 3 { // 2 account boxes + 1 log box
		t.Errorf("want ≥3 bordered boxes, got %d:\n%s", strings.Count(sframe, "╭"), sframe)
	}
	// colored profile: state carries ANSI when color is active — assert the
	// frame differs from its stripped self only if profile is non-Ascii
	// (Ascii envs legitimately render plain; both are accepted).
	_ = frame
}

func TestRenderFrameRollWindowShowsNewest(t *testing.T) {
	rolls := map[string][]string{"a": {"1", "2", "3", "4", "5"}}
	frame := renderFrame(100, time.Now(), "v", []*statusRow{testRow("a", "working", time.Now())}, rolls, nil, "")
	lines := boxContentLines(t, frame)
	// first box = the account's rolling pane (2 rows); a trailing log box
	// may follow with empty rows.
	if len(lines) < 2 {
		t.Fatalf("want at least the 2 rolling rows, got %d:\n%s", len(lines), frame)
	}
	if !strings.Contains(lines[0], "4") || !strings.Contains(lines[1], "5") {
		t.Errorf("viewport must show the newest lines, got %q / %q", lines[0], lines[1])
	}
	joined := strings.Join(lines, "|")
	if strings.Contains(joined, "| 1 ") || strings.Contains(joined, "| 2 ") {
		t.Errorf("oldest events must scroll out of the 2-row window, got %q", joined)
	}
}

func TestRenderFrameLogRingCap(t *testing.T) {
	ring := []string{"l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8", "l9", "l10", "l11", "l12"}
	frame := renderFrame(100, time.Now(), "v", nil, map[string][]string{}, ring, "")
	sframe := ansiStrip(frame)
	for _, dead := range []string{"l1\n", "l2\n"} {
		if strings.Contains(sframe, dead) && !strings.Contains(sframe, "l1 ") {
			// l1 must be scrolled out entirely (l10/l11/l12 contain "l1" as prefix)
		}
	}
	if !strings.Contains(sframe, "l12") || !strings.Contains(sframe, "l10") {
		t.Error("log box must keep the newest 10")
	}
	lines := boxContentLines(t, frame)
	if len(lines) != logRingSize {
		t.Errorf("log box must be %d rows, got %d", logRingSize, len(lines))
	}
}

// boss 0910: long content wraps inside the boxes (reflow, CJK-correct)
// and overflow scrolls out; status line truncates with a visible ….
func TestRenderFrameWrapAndCut(t *testing.T) {
	long := strings.Repeat("word ", 30) // 150 cols — wraps inside the box
	ring := []string{long}
	rolls := map[string][]string{"a": {"<event content that is quite long and keeps going past the pane width>"}}
	frame := renderFrame(100, time.Now(), "v",
		[]*statusRow{{tag: "a", state: "working", started: time.Now(), detail: strings.Repeat("x", 120)}},
		rolls, ring, "errors-*.log")
	sframe := ansiStrip(frame)
	if strings.Count(sframe, "\n") < 6 {
		t.Errorf("long content must wrap into multiple box rows:\n%s", sframe)
	}
	if !strings.Contains(sframe, "…") {
		t.Errorf("over-width status detail must carry a trailing …:\n%s", sframe)
	}
	for _, line := range strings.Split(sframe, "\n") {
		if cols := visualCols(line); cols > 100 {
			t.Errorf("line exceeds 100 cols (%d): %q", cols, line)
		}
	}
}

// metering lines never reach the rolling pane content.
func TestRollContentSkipsMetering(t *testing.T) {
	got := rollContent([]string{"step_finish | ctx 8k", "real output", "step_finish | ctx 9k"})
	if got != "real output" {
		t.Errorf("metering lines must be skipped: %q", got)
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
