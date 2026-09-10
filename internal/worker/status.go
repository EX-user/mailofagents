package worker

// StatusBoard TUI (v0.2.8 upgrade, boss ASCII spec = acceptance baseline;
// right-indent form + hard-cut "…" per boss letter 2026-09-10):
//
//	worker launch at <ts>. version: <buildTag>
//	--------------------------------------------------
//	[addr] waiting up 15m17s | 3 unread | ctx ≈97k
//	    <rolling output, latest last (2 wrappable lines, right-indent,
//	     over-long content hard-cut with trailing …)>
//	[addr2] working up … | thinking… | ctx ≈196k
//	    …
//	--------------------------------------------------
//	[worker-log]
//	    <up to 10 rolling log lines, one line each, hard-cut with …>
//	    full logs: <path>            (hint line, not counted in the 10)
//
// States: waiting | working | compact | error (error = quota/network/wake
// failures — boss detail #2). Status rows stay on one line; rolling rows
// wrap within the two-row window, log lines do not wrap. The frame is
// built by renderFrame as a plain multi-line string — the ANSI draw loop
// prints it in place, and `-tui-screenshot` dumps synthetic frames for
// the bench (boss acceptance detail: TUI "screenshots" without running a
// duty loop).

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wrap"
	"github.com/muesli/termenv"
)

const (
	rollRows    = 2  // rolling output rows per account (boss spec)
	rollEvents  = 8  // recent stream events kept per account (chunked at render)
	logRingSize = 10 // worker-log rolling lines (boss spec)
)

type statusRow struct {
	tag          string
	state        string // "waiting" | "working" | "compact" | "error"
	detail       string
	since        time.Time // when the current state started
	started      time.Time // process start, for uptime
	ctxTokens    int64     // latest context-size report from the CLI (0 = none yet)
	ctxWindow    int64     // configured model window: percentage denominator
	noticeTokens int64     // compact_notice_tokens: fallback denominator
}

type Board struct {
	mu           sync.Mutex
	rows         []*statusRow
	enabled      bool
	drawn        int
	dumpDir      string // WORKER_TUI_DUMP: write frames as files even without a TTY (bench capture)
	launch       time.Time
	version      string
	logHint      string              // full-log path hint line (boss detail #3)
	logRing      []string            // worker-log rolling lines, oldest first
	rowEvents    map[string][]string // per-account recent stream events (raw, newest last)
	lastDump     string              // last dumped frame content (dump mode dedup)
	lastDumpTime int64               // unix nano of last dump (throttle)
	dumped       []string            // dumped frame contents
}

var board = &Board{launch: time.Now(), rowEvents: map[string][]string{}}

// SetMeta feeds the header/version and the full-log hint line (called from
// main before the duty loop).
func (b *Board) SetMeta(version, logHint string) {
	b.mu.Lock()
	b.version = version
	b.logHint = logHint
	b.mu.Unlock()
}

// RenderLoop drives the in-place status board redraw (package-level entry).
func RenderLoop(ctx context.Context) { board.renderLoop(ctx) }

// SetMeta feeds the board header (version) and the full-log hint line
// (package-level entry, called from main before the duty loop).
func SetMeta(version, logHint string) { board.SetMeta(version, logHint) }

func init() {
	// Enabled only on a TTY; WORKER_PLAIN=1 force-disables (files, pipes,
	// awkward terminals).
	if fi, err := os.Stdout.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		board.enabled = true
	}
	if os.Getenv("WORKER_PLAIN") == "1" {
		board.enabled = false
	}
	// WORKER_TUI_DUMP: frame dumps on state changes even without a TTY —
	// the bench captures REAL-run frames through it (boss acceptance).
	board.dumpDir = os.Getenv("WORKER_TUI_DUMP")
	// Bench dump frames stay plain text (grep-able, diff-able) — color
	// only travels to a real terminal or the forced-profile screenshot.
	if board.dumpDir != "" && !board.enabled {
		lipgloss.SetColorProfile(termenv.Ascii)
	}
}

// AddRow registers one account line at board creation time. ctxWindow /
// noticeTokens are the percentage denominators for the ctx readout (window
// wins; notice is the fallback; neither = absolute tokens only).
func (b *Board) AddRow(tag string, started time.Time, ctxWindow, noticeTokens int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rows = append(b.rows, &statusRow{
		tag: tag, state: "waiting", since: started, started: started,
		ctxWindow: ctxWindow, noticeTokens: noticeTokens,
	})
}

// SetCtx records the CLI's latest context-size report for the row's live
// ctx readout (rendered by the next draw tick).
func (b *Board) SetCtx(tag string, tokens int64) {
	b.mu.Lock()
	if row := b.row(tag); row != nil {
		row.ctxTokens = tokens
	}
	b.mu.Unlock()
	if b.dumpDir != "" {
		b.render()
	}
}

// Set updates a row's state/detail. State "" = streaming output summary:
// it rolls into the account's two-line output area instead of the row line.
func (b *Board) Set(tag, state, detail string) {
	b.mu.Lock()
	row := b.row(tag)
	if row != nil {
		if state == "" {
			// streaming output: keep recent raw events; the render chunks
			// them into the two-line horizontal continuation window
			evs := append(b.rowEvents[tag], detail)
			if len(evs) > rollEvents {
				evs = evs[len(evs)-rollEvents:]
			}
			b.rowEvents[tag] = evs
			row.detail = ""
		} else {
			if row.state != state || detail != row.detail {
				row.since = time.Now()
			}
			row.state = state
			row.detail = detail
			delete(b.rowEvents, tag) // state change: stale stream fragments go
		}
	}
	b.mu.Unlock()
	if b.enabled || b.dumpDir != "" {
		b.render()
	}
}

func (b *Board) row(tag string) *statusRow {
	for _, r := range b.rows {
		if r.tag == tag {
			return r
		}
	}
	return nil
}

// Logf records a line into the worker-log rolling pane (in-place redraw;
// nothing prints above the board anymore — boss spec: errors scroll at the
// bottom, never stack). When the board is disabled it falls back to plain
// log.Printf so redirected runs keep a flat log.
func (b *Board) Logf(tag, format string, args ...any) {
	line := fmt.Sprintf("[%s] %s", tag, fmt.Sprintf(format, args...))
	b.mu.Lock()
	if b.enabled || b.dumpDir != "" {
		b.logRing = append(b.logRing, line)
		if len(b.logRing) > logRingSize {
			b.logRing = b.logRing[len(b.logRing)-logRingSize:]
		}
		b.mu.Unlock()
		b.render()
		return
	}
	b.mu.Unlock()
	log.Print(line)
}

// render redraws the board (locks; for use outside Logf).
func (b *Board) render() {
	b.mu.Lock()
	w := consoleWidth()
	if w < 20 {
		w = 80
	}
	rows := append([]*statusRow(nil), b.rows...)
	rolls := map[string][]string{}
	for k, v := range b.rowEvents {
		rolls[k] = append([]string(nil), v...)
	}
	ring := append([]string(nil), b.logRing...)
	launch, version, hint := b.launch, b.version, b.logHint
	b.mu.Unlock()

	frame := renderFrame(w, launch, version, rows, rolls, ring, hint)
	if b.enabled {
		b.drawFrame(frame)
	}
	b.dumpFrame(frame)
}

// drawFrame prints a frame in place (erase previous + repaint).
func (b *Board) drawFrame(frame string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.erase()
	for _, line := range strings.Split(frame, "\n") {
		fmt.Fprintf(os.Stdout, "\r\033[2K%s\n", line)
		b.drawn++
	}
}

// dumpFrame writes the frame to WORKER_TUI_DUMP when its content changed —
// the bench's real-run screenshot capture.
func (b *Board) dumpFrame(frame string) {
	if b.dumpDir == "" {
		return
	}
	b.mu.Lock()
	now := time.Now()
	if frame == b.lastDump || now.UnixNano()-b.lastDumpTime < 15*time.Second.Nanoseconds() {
		// identical frame, or a same-state frame within the throttle
		// window: the board ticks every 500ms and uptime/age make every
		// tick textually unique — without this gate one real wake would
		// flood the dump dir with near-identical frames.
		if frame != b.lastDump {
			b.lastDump = frame
		}
		b.mu.Unlock()
		return
	}
	b.lastDump = frame
	b.lastDumpTime = now.UnixNano()
	n := len(b.dumped) + 1
	state := ""
	if r := b.rows; len(r) > 0 {
		// name frames after the first row's state for scanability
		state = r[0].state
	}
	b.dumped = append(b.dumped, frame)
	dir := b.dumpDir
	b.mu.Unlock()
	os.MkdirAll(dir, 0o755)
	name := fmt.Sprintf("%s/frame-%02d-%s.txt", dir, n, strings.ReplaceAll(state, " ", "_"))
	_ = os.WriteFile(name, []byte(frame), 0o644)
}

// erase lifts the cursor above the drawn board and clears downward.
func (b *Board) erase() {
	if b.drawn > 0 {
		fmt.Fprintf(os.Stdout, "\033[%dA\033[J", b.drawn)
	}
	b.drawn = 0
}

// (draw was folded into render + drawFrame: the frame is built once and
// either printed in place or dumped to WORKER_TUI_DUMP.)

// renderFrame builds the whole board as a string — the single source of
// the layout, shared by the live draw loop, WORKER_TUI_DUMP and the
// -tui-screenshot dumps (boss acceptance: bench-viewable frames).
// Library form per boss 0910 directive: the rolling area and the
// worker-log pane are text-box widgets (bubbles viewport inside a
// lipgloss rounded border, content wrapped by reflow — grapheme-correct
// CJK), overflow scrolls up out of the window; the status line carries a
// state-colored dot (green waiting / blue working / yellow compact /
// red error).
func renderFrame(w int, launch time.Time, version string, rows []*statusRow, rolls map[string][]string, logRing []string, logHint string) string {
	// Defensive caps: renderFrame is the layout authority even when callers
	// bypass Set/Logf.
	if len(logRing) > logRingSize {
		logRing = logRing[len(logRing)-logRingSize:]
	}
	capped := map[string][]string{}
	for k, v := range rolls {
		if len(v) > rollEvents {
			v = v[len(v)-rollEvents:]
		}
		capped[k] = v
	}
	rolls = capped

	sep := strings.Repeat("─", min2(w, 100))
	var bld strings.Builder
	fmt.Fprintf(&bld, "%s\n", clampCols(fmt.Sprintf("worker launch at %s. version: %s",
		launch.Format("2006/01/02 15:04:05"), version), w))
	bld.WriteString(sep + "\n")
	for _, r := range rows {
		fmt.Fprintf(&bld, "%s\n", statusLine(r, w))
		fmt.Fprintf(&bld, "%s\n", indentBlock(textBox(rollContent(rolls[r.tag]), rollRows, w-2), 2))
	}
	bld.WriteString(sep + "\n")
	bld.WriteString("[worker-log]\n")
	fmt.Fprintf(&bld, "%s\n", textBox(strings.Join(logRing, "\n"), logRingSize, w))
	if logHint != "" {
		fmt.Fprintf(&bld, "    %s\n", clampCols("full logs: "+logHint, max2(w-4, 10)))
	}
	return strings.TrimRight(bld.String(), "\n")
}

// statusLine renders one account row: a state-colored dot plus the
// uppercase state, with detail/ctx clamped so the line never exceeds w.
func statusLine(r *statusRow, w int) string {
	st := stateStyle(r.state)
	head := fmt.Sprintf("%s [%s] %s · up %s",
		st.Render("●"), r.tag, st.Render(strings.ToUpper(r.state)),
		time.Since(r.started).Round(time.Second))
	tail := ""
	if r.ctxTokens > 0 {
		tail = " | ctx " + ctxReadout(r.ctxTokens, r.ctxWindow, r.noticeTokens)
	}
	detail := r.detail
	if detail == "" {
		if lipgloss.Width(head+tail) > w {
			return clampCols(head, w)
		}
		return head + tail
	}
	budget := w - lipgloss.Width(head) - lipgloss.Width(tail) - 3 // " | "
	if budget < 4 {
		return clampCols(head+tail, w)
	}
	return head + " | " + clampEllipsis(detail, budget) + tail
}

// stateStyle maps a board state to its color (boss 0910: green waiting,
// blue working; compact yellow, error red).
func stateStyle(state string) lipgloss.Style {
	var c lipgloss.Color
	switch state {
	case "waiting":
		c = lipgloss.Color("2")
	case "working":
		c = lipgloss.Color("4")
	case "compact":
		c = lipgloss.Color("3")
	case "error":
		c = lipgloss.Color("1")
	default:
		c = lipgloss.Color("7")
	}
	return lipgloss.NewStyle().Foreground(c).Bold(true)
}

// textBox renders content as a text-box widget: a viewport of the given
// height inside a lipgloss rounded border. Content wraps at the box's
// inner width (reflow — grapheme-correct CJK widths); overflow lines
// scroll up out of the window, newest lines stay visible.
func textBox(content string, height, outerW int) string {
	inner := outerW - 4 // border(2) + padding(0,1)(2)
	if inner < 10 {
		inner = 10
	}
	vp := viewport.New(inner, height)
	vp.SetContent(wrap.String(content, inner))
	vp.GotoBottom()
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("8")).
		Padding(0, 1).
		Width(outerW - 2).
		Render(vp.View())
}

// indentBlock prefixes every line with n spaces (right-indent for the
// per-account rolling box).
func indentBlock(s string, n int) string {
	pad := strings.Repeat(" ", n)
	return pad + strings.ReplaceAll(s, "\n", "\n"+pad)
}

// rollContent selects the rolling pane's content: recent stream events,
// newest last, metering lines skipped (they duplicate the row's ctx
// readout). Wrapping and windowing are the text box's job now.
func rollContent(events []string) string {
	var keep []string
	for _, ev := range events {
		if strings.HasPrefix(ev, "step_finish") {
			continue
		}
		keep = append(keep, strings.ReplaceAll(ev, "\n", " "))
	}
	return strings.Join(keep, "\n")
}

func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max2(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ctxReadout renders the context usage: a percentage against the configured
// model window (or the notice threshold as the fallback denominator), else
// just the absolute token count.
func ctxReadout(tokens, ctxWindow, noticeTokens int64) string {
	denom := ctxWindow
	if denom <= 0 {
		denom = noticeTokens
	}
	if denom > 0 {
		return fmt.Sprintf("%d%%", int(float64(tokens)/float64(denom)*100+0.5))
	}
	return "≈" + humanTokens(tokens)
}

// clampCols cuts s to occupy at most w terminal columns, counting East
// Asian wide runes as two columns.
func clampCols(s string, w int) string {
	used := 0
	for i, r := range s {
		cw := runeWidth(r)
		if used+cw > w {
			return s[:i]
		}
		used += cw
	}
	return s
}

// clampEllipsis is clampCols with a visible cut: an over-width string is
// hard-truncated and a trailing "…" marks the elision (boss 0910 spec:
// 硬截断+"…"). Fits rune and the ellipsis inside w columns.
func clampEllipsis(s string, w int) string {
	total := 0
	for _, r := range s {
		total += runeWidth(r)
	}
	if total <= w {
		return s
	}
	used := 0
	for i, r := range s {
		if used+runeWidth(r) > w-1 { // reserve one column for "…"
			return s[:i] + "…"
		}
		used += runeWidth(r)
	}
	return s
}

// runeWidth approximates a rune's terminal column count (2 for the common
// East Asian wide/fullwidth ranges, else 1).
func runeWidth(r rune) int {
	switch {
	case r == 0x2329 || r == 0x232A,
		r >= 0x1100 && r <= 0x115F,
		r >= 0x2E80 && r <= 0xA4CF && r != 0x303F,
		r >= 0xAC00 && r <= 0xD7A3,
		r >= 0xF900 && r <= 0xFAFF,
		r >= 0xFE30 && r <= 0xFE6F,
		r >= 0xFF00 && r <= 0xFF60,
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x20000 && r <= 0x3FFFD:
		return 2
	}
	return 1
}

// SprintDetail clamps a live summary to the restrained width.
func SprintDetail(s string) string {
	return truncate(strings.ReplaceAll(s, "\n", " "), 100)
}

// renderLoop redraws the board periodically so uptime clocks tick and
// waiting rows stay visible without new log lines. Call once from main.
func (b *Board) renderLoop(ctx context.Context) {
	if !b.enabled {
		return
	}
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.render()
		}
	}
}
