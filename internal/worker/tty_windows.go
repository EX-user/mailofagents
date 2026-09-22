//go:build windows

package worker

import (
	"os"

	"github.com/charmbracelet/x/term"
)

// openTty opens the Windows console input buffer and enables VT input so
// the terminal reports mouse clicks/motion as SGR sequences (classic conhost
// delivers them as Win32 INPUT_RECORDs otherwise, which this reader cannot
// parse; Windows Terminal honours VT input).
func openTty() (*os.File, func(), error) {
	tty, err := os.OpenFile("CONIN$", os.O_RDONLY, 0)
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
