package worker

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// mouse controls (boss 0.2.10 pool, sign-off 2026-09-22): SGR click tracking
// (ESC[?1000h + ESC[?1006h) over a dedicated /dev/tty reader — the CLI child
// processes keep stdin, so terminal input never touches the worker's own
// stdin. Clicks map to account-row buttons (停止/压缩) via a per-frame hit
// map; the board's absolute top row is resolved once through a CPR
// (ESC[6n) round-trip right after the first frame is drawn.

type boardAction struct {
	Tag  string // account local-part
	Kind string // "stop" | "compact" | "copy"
}

const (
	ctlStop    = "[停止]"
	ctlCompact = "[压缩]"
	ctlCopy    = "[复制]"
	ctlText    = "  " + ctlStop + " " + ctlCompact + " " + ctlCopy

	mouseEnable  = "\x1b[?1000h\x1b[?1003h\x1b[?1006h" // click + any-motion (hover highlight)
	mouseDisable = "\x1b[?1000l\x1b[?1006l"
	cprQuery     = "\x1b[6n" // terminal replies ESC[row;colR on the tty
)

// rowHit records where one account row's buttons live: the board-relative
// line index of the row (1-based) and the display-column ranges (1-based,
// inclusive) of each button.
type rowHit struct {
	line      int
	stopAt    int
	stopEnd   int
	compactAt int
	compactEnd int
	copyAt    int
	copyEnd   int
}

// --- action pub/sub (Board → Duty) ---

// SubscribeActions returns a channel receiving actions for one account tag.
// The channel lives for the process (duty loops never unsubscribe).
func (b *Board) SubscribeActions(tag string) <-chan boardAction {
	b.actionsMu.Lock()
	defer b.actionsMu.Unlock()
	ch := make(chan boardAction, 8)
	b.actionSubs[tag] = append(b.actionSubs[tag], ch)
	return ch
}

// RequestAction fans an action out to every subscriber of the tag (and the
// "*" wildcard, for tests). Non-blocking: a full channel drops the click.
func (b *Board) RequestAction(tag, kind string) {
	b.actionsMu.Lock()
	defer b.actionsMu.Unlock()
	a := boardAction{Tag: tag, Kind: kind}
	subs := append(append([]chan boardAction{}, b.actionSubs[tag]...), b.actionSubs["*"]...)
	for _, ch := range subs {
		select {
		case ch <- a:
		default:
		}
	}
}

// copySession hands the bound session id to the terminal clipboard via
// OSC52 (ESC]52;c;<base64> BEL). OSC52 is the only channel that survives
// SSH without a local helper; terminals that filter it (some Windows
// terminals, tmux without set-clipboard) simply show nothing — there is no
// reliable in-band fallback, so the log line always confirms what was
// attempted and the id stays visible for manual copy.
func (b *Board) copySession(session string) {
	if session == "" {
		return
	}
	enc := base64.StdEncoding.EncodeToString([]byte(session))
	fmt.Fprintf(os.Stdout, "\x1b]52;c;%s\x07", enc)
}

// EnableMouse switches the control plane on: SGR tracking on the terminal
// plus a /dev/tty reader goroutine (cancelled with ctx). No-op when the
// board is off or the config toggle is unset.
func (b *Board) EnableMouse(ctx context.Context) {
	b.mu.Lock()
	on := b.mouse && b.enabled
	b.mu.Unlock()
	if !on {
		return
	}
	fmt.Fprint(os.Stdout, "\x1b[2J\x1b[H"+mouseEnable)
	// the clear homes the cursor: the board's top row is deterministically
	// row 1 (kills the whole cursor-math offset class — boss demo round 5).
	// No CPR refinement here: any cursor probe sampled between differential
	// ticks reads the wrong row and would overwrite this correct value
	// (boss WSL demo: top=-2, buttons inert). Resize full repaints also
	// re-home, so topRow stays 1 for the board's whole life.
	b.mu.Lock()
	b.topRow = 1
	b.mu.Unlock()
	go b.startInput(ctx)
}

// resolveTopRow is the retired CPR-based top-row probe, kept for reference:
// it raced differential repaints (any sample between ticks reads a stale
// cursor row) and has been superseded by EnableMouse's clear-and-home,
// which pins topRow=1 unconditionally.
//
//nolint:unused
func (b *Board) resolveTopRow(ctx context.Context) {
	for attempt := 0; attempt < 10; attempt++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
		b.mu.Lock()
		drawn := b.drawn
		b.mu.Unlock()
		if drawn == 0 {
			continue // first frame not out yet
		}
		fmt.Fprint(os.Stdout, cprQuery)
		select {
		case <-ctx.Done():
			return
		case row := <-b.cprCh:
			b.mu.Lock()
			b.topRow = row - drawn
			b.mu.Unlock()
			return
		case <-time.After(2 * time.Second):
			// terminal never answered: retry, then give up (buttons stay
			// inert rather than mis-firing on a wrong top row)
		}
	}
}

// --- tty input ---

func (b *Board) readTty(ctx context.Context, tty *os.File, restore func()) {
	defer tty.Close()
	defer restore()
	defer fmt.Fprint(os.Stdout, mouseDisable)
	buf := make([]byte, 0, 256)
	chunk := make([]byte, 64)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		n, err := tty.Read(chunk)
		if n > 0 {
			// Raw mode disabled ISIG, so Ctrl+C arrives as the plain byte
			// 0x03 — deliver it as a real SIGINT (main runs a NotifyContext
			// on it: this is the graceful-shutdown path, not an abort).
			if bytes.IndexByte(chunk[:n], 0x03) >= 0 {
				restore()
				proc, _ := os.FindProcess(os.Getpid())
				proc.Signal(syscall.SIGINT)
				return
			}
			buf = b.consumeInput(chunk[:n], buf)
		}
		if err != nil {
			return
		}
	}
}

// consumeInput appends chunk to buf and extracts every complete sequence,
// returning the unconsumed tail. Recognized: SGR mouse presses
// (ESC[<b;x;yM), cursor-position reports (ESC[row;colR). Incomplete tails
// are held until more bytes arrive.
func (b *Board) consumeInput(chunk []byte, buf []byte) []byte {
	b.inputCount.Add(int64(len(chunk)))
	buf = append(buf, chunk...)
	for {
		s := string(buf)
		if i := strings.Index(s, "\x1b[<"); i >= 0 {
			end := strings.IndexAny(s[i:], "Mm")
			if end < 0 {
				return buf // incomplete
			}
			body := s[i+3 : i+end]
			parts := strings.Split(body, ";")
			if s[i+end] == 'M' && len(parts) == 3 {
				btn, _ := strconv.Atoi(parts[0])
				col, _ := strconv.Atoi(parts[1])
				row, _ := strconv.Atoi(parts[2])
				switch {
				case btn == 0:
					b.click(col, row)
				case btn >= 32:
					// motion without buttons (SGR: 32 = no button): hover
					// highlight for the row under the pointer (boss demo
					// feedback 2026-09-22: no hover feedback = feels dead)
					b.hover(col, row)
				}
			}
			buf = buf[i+end+1:]
			continue
		}
		if i := strings.Index(s, "\x1b["); i >= 0 {
			end := strings.Index(s[i:], "R")
			if end < 0 {
				return buf // incomplete
			}
			body := s[i+2 : i+end]
			parts := strings.Split(body, ";")
			if len(parts) == 2 {
				row, err1 := strconv.Atoi(parts[0])
				_, err2 := strconv.Atoi(parts[1])
				if err1 == nil && err2 == nil {
					select {
					case b.cprCh <- row:
					default:
					}
				}
				buf = buf[i+end+1:]
				continue
			}
			return buf // unhandled CSI: hold (it may still be a prefix)
		}
		return buf
	}
}

// hover tracks which account row — and which button on it — the pointer
// is over; changes flip that button into reverse video on the next draw.
func (b *Board) hover(col, row int) {
	b.mu.Lock()
	top := b.topRow
	hits := b.hitRows
	b.mu.Unlock()
	if top <= 0 {
		return
	}
	line := row - top + 1
	tag, btn := "", ""
	for t, h := range hits {
		if h.line != line {
			continue
		}
		tag = t
		switch {
		case col >= h.stopAt && col <= h.stopEnd:
			btn = "stop"
		case col >= h.compactAt && col <= h.compactEnd:
			btn = "compact"
		case col >= h.copyAt && col <= h.copyEnd:
			btn = "copy"
		}
	}
	curTag, _ := b.hoverTag.Load().(string)
	curBtn, _ := b.hoverBtn.Load().(string)
	if curTag == tag && curBtn == btn {
		return
	}
	b.hoverTag.Store(tag)
	b.hoverBtn.Store(btn)
	b.render() // immediate feedback; the tick would catch it anyway
}

// click maps absolute terminal coordinates to a row button and dispatches.
func (b *Board) click(col, row int) {
	b.mu.Lock()
	top := b.topRow
	hits := make(map[string]rowHit, len(b.hitRows))
	for k, v := range b.hitRows {
		hits[k] = v
	}
	b.mu.Unlock()
	if top <= 0 {
		return
	}
	line := row - top + 1
	for tag, h := range hits {
		if h.line != line {
			continue
		}
		switch {
		case col >= h.stopAt && col <= h.stopEnd:
			b.RequestAction(tag, "stop")
		case col >= h.compactAt && col <= h.compactEnd:
			b.RequestAction(tag, "compact")
		case col >= h.copyAt && col <= h.copyEnd:
			b.RequestAction(tag, "copy")
		}
	}
}

// --- hit-map construction ---

// computeHits scans rendered frame lines for account rows carrying the
// control text. The row prefix ("● [tag] STATE …") may contain ANSI around
// the state word, so display columns are measured with lipgloss.Width; the
// control text itself is plain.
func computeHits(lines []string) map[string]rowHit {
	hits := map[string]rowHit{}
	for i, ln := range lines {
		// match on the ANSI-stripped text: a hovered row renders its
		// button labels in reverse video (ANSI INSIDE the brackets), so
		// the raw line no longer contains the plain control sequence
		plain := ansiPlain(ln)
		idx := strings.Index(plain, ctlText)
		if idx < 0 {
			continue
		}
		tag, ok := rowTagFromPlain(plain)
		if !ok {
			continue
		}
		w := lipgloss.Width(plain[:idx])
		h := rowHit{line: i + 1}
		h.stopAt = w + 3 // "  [" — first glyph of 停
		h.stopEnd = h.stopAt + lipgloss.Width(ctlStop) - 1
		h.compactAt = h.stopEnd + 2 // "] " between the buttons
		h.compactEnd = h.compactAt + lipgloss.Width(ctlCompact) - 1
		h.copyAt = h.compactEnd + 2
		h.copyEnd = h.copyAt + lipgloss.Width(ctlCopy) - 1
		hits[tag] = h
	}
	return hits
}

// displayWidth measures a string's terminal cell width (wide runes count
// double), skipping ANSI sequences.
func displayWidth(s string) int { return lipgloss.Width(s) }

// ansiPlain strips ANSI control sequences (the display text underneath).
func ansiPlain(s string) string {
	return ansiSeqRe.ReplaceAllString(s, "")
}

var ansiSeqRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// rowTagFromPlain returns the account tag from an ANSI-stripped status row
// ("● [tag] STATE …").
func rowTagFromPlain(plain string) (string, bool) {
	open := strings.Index(plain, "[") // ASCII: byte index safe (● is 3 bytes)
	if open < 0 {
		return "", false
	}
	rest := plain[open+1:]
	end := strings.Index(rest, "]")
	if end <= 0 {
		return "", false
	}
	return rest[:end], true
}

// BoardAction exposes the action payload type to external Board drivers
// (demo-worker) via a type alias.
type BoardAction = boardAction
