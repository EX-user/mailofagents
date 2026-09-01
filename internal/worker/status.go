package worker

// StatusBoard: one fixed bottom line per account, redrawn in place
// (ANSI) — working rows stream the latest model-output summary, waiting
// rows show a live uptime/read count. Regular log lines print above the
// board (the board is erased before a line and redrawn after). When stdout
// is not a TTY (redirected to a file) the board disables itself and
// summaries fall back to plain log lines.

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

type statusRow struct {
	tag     string
	state   string // "waiting" | "working"
	detail  string
	since   time.Time // when the current state started
	started time.Time // process start, for uptime
}

type Board struct {
	mu      sync.Mutex
	rows    []*statusRow
	enabled bool
	drawn   int
}

var board = &Board{}

// RenderLoop drives the in-place status board redraw (package-level entry).
func RenderLoop(ctx context.Context) { board.RenderLoop(ctx) }

func init() {
	// Enabled only on a TTY; WORKER_PLAIN=1 force-disables (files, pipes,
	// awkward terminals).
	if fi, err := os.Stdout.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		board.enabled = true
	}
	if os.Getenv("WORKER_PLAIN") == "1" {
		board.enabled = false
	}
}

// AddRow registers one account line at board creation time.
func (b *Board) AddRow(tag string, started time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rows = append(b.rows, &statusRow{tag: tag, state: "waiting", since: started, started: started})
}

// Set updates a row's state/detail.
func (b *Board) Set(tag, state, detail string) {
	b.mu.Lock()
	row := b.row(tag)
	if row != nil {
		if row.state != state || detail != row.detail {
			row.since = time.Now()
		}
		if state != "" {
			row.state = state
		}
		row.detail = detail
	}
	b.mu.Unlock()
	if board.enabled {
		board.render()
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

// Logf prints a normal log line above the board (erase board → line →
// redraw), then refreshes. The explicit erase() first moves the cursor
// above the board so the log line lands where the board stood; draw()'s
// internal erase is then a no-op (drawn==0) and just repaints.
func (b *Board) Logf(tag, format string, args ...any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.enabled {
		b.erase()
	}
	log.Printf("["+tag+"] "+format, args...)
	if b.enabled {
		b.draw()
	}
}

// render redraws the board (locks; for use outside Logf).
func (b *Board) render() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.enabled {
		b.draw()
	}
}

// erase lifts the cursor above the drawn board and clears downward.
func (b *Board) erase() {
	if b.drawn > 0 {
		fmt.Fprintf(os.Stdout, "\033[%dA\033[J", b.drawn)
	}
	b.drawn = 0
}

// draw repaints the board in place: lift the cursor above the rows drawn
// last time, clear down, then print one line per account. Erase+repaint is
// one atomic op here — every caller (Logf, Set, RenderLoop) goes through
// draw, so the board never grows a new line per tick. Each row is clamped
// to the terminal width: a row that wraps onto a second physical line
// breaks the erase cursor arithmetic and resurrects stale rows.
func (b *Board) draw() {
	b.erase()
	w := consoleWidth()
	if w < 20 {
		w = 80
	}
	for _, r := range b.rows {
		up := time.Since(r.started).Round(time.Second)
		line := fmt.Sprintf("[%s] %-7s up %s", r.tag, r.state, up)
		if r.detail != "" {
			line += " | " + r.detail
		}
		fmt.Fprintf(os.Stdout, "\r\033[2K%s\n", clampCols(line, w))
		b.drawn++
	}
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

// RenderLoop redraws the board periodically so uptime clocks tick and
// waiting rows stay visible without new log lines. Call once from main.
func (b *Board) RenderLoop(ctx context.Context) {
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
			b.mu.Lock()
			b.draw()
			b.mu.Unlock()
		}
	}
}
