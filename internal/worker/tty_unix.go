//go:build unix

package worker

import (
	"context"
	"os"

	"github.com/charmbracelet/x/term"
)

// openTty opens the controlling terminal and puts it in raw mode (mouse
// and CPR replies arrive as immediate byte sequences, not lines). The
// returned restore func MUST run when reading stops.
func openTty() (*os.File, func(), error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDONLY, 0)
	if err != nil {
		return nil, nil, err
	}
	old, err := term.MakeRaw(tty.Fd())
	if err != nil {
		tty.Close()
		return nil, nil, err
	}
	return tty, func() { term.Restore(tty.Fd(), old) }, nil
}

// startInput is the EnableMouse hook (platform dispatch): raw-mode byte
// reader over /dev/tty.
func (b *Board) startInput(ctx context.Context) {
	tty, restore, err := openTty()
	if err != nil {
		return
	}
	b.readTty(ctx, tty, restore)
}

// WinDiag is a no-op on unix (no record reader); unix diagnostics ride the
// byte counter.
func WinDiag() (recs, keys, mouse int64, mode int64) { return -1, -1, -1, -1 }
