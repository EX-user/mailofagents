//go:build windows

package worker

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// Windows console input (boss demo feedback 2026-09-22, second round):
// classic conhost delivers mouse as Win32 INPUT_RECORDs, never as VT byte
// sequences — so the unix byte reader is replaced here by a
// ReadConsoleInput loop that dispatches clicks/hover directly.

const (
	winEnableMouseInput   = 0x0010
	winEnableWindowInput  = 0x0008
	winEnableExtendedOpts = 0x0080
	winQuickEditMode      = 0x0040 // off: QuickEdit swallows mouse + blocks output
	winFromLeft1stButton  = 0x0001
	winMouseMoved         = 0x0001
	winKeyEvent           = 0x0001
	winMouseEvent         = 0x0002
)

type coord struct{ X, Y int16 }

type inputRecord struct {
	EventType uint16
	_         uint16
	Event     [16]byte
}

type mouseEventRecord struct {
	X, Y            int16
	ButtonState     uint32
	ControlKeyState uint32
	EventFlags      uint32
}

// diagnostic counters (boss demo round 3: events=0 while Shift-select was
// active — need to see whether records arrive at all and of which type)
var (
	recCount  = new(int64)
	keyCount  = new(int64)
	mouseIn   = new(int64)
	modeSeen  = new(int64)
)

func addi(p *int64, v int64) { for { c := *p; if atomic.CompareAndSwapInt64(p, c, c+v) { return } } }

var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procReadConsoleIn  = kernel32.NewProc("ReadConsoleInputW")
	procSetConsoleMode = kernel32.NewProc("SetConsoleMode")
	procGetConsoleMode = kernel32.NewProc("GetConsoleMode")
)

// readConsoleEvents drives the whole Windows input plane: opens CONIN$,
// enables mouse+window input (QuickEdit off), then loops ReadConsoleInputW
// dispatching clicks and hover straight into the board. Blocks until ctx
// is done or the console fails.
func readConsoleEvents(ctx context.Context, b *Board) {
	// CONIN$ needs read+write for the mode dance (read-only handles have
	// been observed dying between GetConsoleMode and the read loop — boss
	// demo round 3: recs=0 with mode visible)
	tty, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err != nil {
		winFail("open CONIN$: " + err.Error())
		return
	}
	defer tty.Close()
	fd := tty.Fd()
	var mode uint32
	if r, _, e := procGetConsoleMode.Call(fd, uintptr(unsafe.Pointer(&mode))); r == 0 {
		winFail("GetConsoleMode: " + e.Error())
		return
	}
	addi(modeSeen, int64(mode))
	want := (mode &^ winQuickEditMode) | winEnableMouseInput | winEnableWindowInput | winEnableExtendedOpts
	if r, _, e := procSetConsoleMode.Call(fd, uintptr(want)); r == 0 {
		winFail(fmt.Sprintf("SetConsoleMode(0x%x): %v", want, e))
		return
	}
	defer func() { procSetConsoleMode.Call(fd, uintptr(mode)) }() // restore

	rec := inputRecord{}
	var read uint32
	fails := 0
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		r, _, e := procReadConsoleIn.Call(fd, uintptr(unsafe.Pointer(&rec)), 1, uintptr(unsafe.Pointer(&read)))
		if r == 0 {
			fails++
			winFail(fmt.Sprintf("ReadConsoleInputW: %v", e))
			if fails > 5 {
				return
			}
			time.Sleep(200 * time.Millisecond)
			continue
		}
		fails = 0
		if read == 0 {
			continue
		}
		addi(recCount, 1)
		if rec.EventType == winKeyEvent {
			addi(keyCount, 1)
		}
		if rec.EventType != winMouseEvent {
			continue
		}
		addi(mouseIn, 1)
		b.inputCount.Add(1)
		var me mouseEventRecord
		copy((*[16]byte)(unsafe.Pointer(&me))[:], rec.Event[:])
		col, row := int(me.X)+1, int(me.Y)+1
		switch {
		case me.EventFlags&winMouseMoved != 0:
			b.hover(col, row)
		case me.ButtonState&winFromLeft1stButton != 0:
			b.click(col, row)
		}
	}
}

// startInput is the EnableMouse hook (platform dispatch).
func (b *Board) startInput(ctx context.Context) { readConsoleEvents(ctx, b) }

var winErrText atomic.Value // string: where the input plane died ("" alive)

func winFail(msg string) { winErrText.Store(msg) }

// WinDiag exposes the record-level counters for the demo driver's
// diagnostics line (recs/keys/mouse counts and the console mode we saw).
func WinDiag() (recs, keys, mouse int64, mode int64, errText string) {
	return atomic.LoadInt64(recCount), atomic.LoadInt64(keyCount),
		atomic.LoadInt64(mouseIn), atomic.LoadInt64(modeSeen),
		func() string { s, _ := winErrText.Load().(string); return s }()
}
