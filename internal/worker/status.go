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

// renderFrame builds the whole board as a plain string (no ANSI) — the
// single source of the layout, shared by the live draw loop and the
// -tui-screenshot dumps (boss acceptance: bench-viewable frames).
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
	sep := strings.Repeat("-", min2(w, 100))
	var bld strings.Builder
	fmt.Fprintf(&bld, "%s\n", clampCols(fmt.Sprintf("worker launch at %s. version: %s",
		launch.Format("2006/01/02 15:04:05"), version), w))
	bld.WriteString(sep + "\n")
	for _, r := range rows {
		up := time.Since(r.started).Round(time.Second)
		line := fmt.Sprintf("[%s] %s · up %s", r.tag, strings.ToUpper(r.state), up)
		if r.detail != "" {
			line += " | " + r.detail
		}
		if r.ctxTokens > 0 {
			line += " | ctx " + ctxReadout(r.ctxTokens, r.ctxWindow, r.noticeTokens)
		}
		fmt.Fprintf(&bld, "%s\n", clampEllipsis(line, w))
		for _, out := range rollWindow(rolls[r.tag], max2(w-4, 10), rollRows) {
			// boss 0910 spec: the rolling two rows are plain right-indent —
			// separation from line start, no gutter glyph.
			fmt.Fprintf(&bld, "    %s\n", clampCols(out, max2(w-4, 10)))
		}
	}
	bld.WriteString(sep + "\n")
	bld.WriteString("[worker-log]\n")
	for _, l := range logRing {
		// boss 0910 correction: log lines do NOT wrap — one line each,
		// hard cut with a trailing "…" when over width.
		fmt.Fprintf(&bld, "    %s\n", clampEllipsis(l, max2(w-4, 10)))
	}
	if logHint != "" {
		fmt.Fprintf(&bld, "    full logs: %s\n", clampCols(logHint, max2(w-4, 10)))
	}
	return strings.TrimRight(bld.String(), "\n")
}

// rollWindow renders the two-line rolling area as a horizontal
// continuation window over recent stream events (boss feedback
// 2026-09-08; cut form per boss 0910 letter: wrap what fits, then a hard
// cut with a trailing "…" — head shown, tail elided). Metering lines
// that merely duplicate the row's ctx readout are skipped.
func rollWindow(events []string, width, rows int) []string {
	var pieces []string
	for i := len(events) - 1; i >= 0 && len(pieces) < rows; i-- {
		ev := strings.ReplaceAll(events[i], "\n", " ")
		if strings.HasPrefix(ev, "step_finish") {
			continue // metering: duplicates the row's ctx readout
		}
		chunks := chunkCols(ev, width)
		room := rows - len(pieces)
		if len(chunks) > room {
			// hard cut: clamp the whole item to room*width columns with a
			// trailing "…" (the ellipsis is part of the clamp budget), then
			// re-chunk so every row still fits the width exactly.
			ev = clampEllipsis(ev, room*width)
			chunks = chunkCols(ev, width)
		}
		pieces = append(chunks, pieces...)
		if len(pieces) >= rows {
			break
		}
	}
	for len(pieces) < rows {
		pieces = append(pieces, "")
	}
	return pieces[:rows]
}

// chunkCols splits s into width-column chunks (wide-rune aware).
func chunkCols(s string, w int) []string {
	if w < 10 {
		w = 10
	}
	var out []string
	cur, used := 0, 0
	for i, r := range s {
		cw := runeWidth(r)
		if used+cw > w {
			out = append(out, s[cur:i])
			cur, used = i, 0
		}
		used += cw
	}
	return append(out, s[cur:])
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
