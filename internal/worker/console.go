package worker

import (
	"os"

	"github.com/charmbracelet/x/term"
)

// consoleSize reports stdout's terminal dimensions in cells (0,0 if
// unknown). Re-read every render tick — the resize detector compares it
// against the last seen size (cross-platform: SIGWINCH only exists on
// unix, but a size change is detectable everywhere; boss demo feedback
// 2026-09-22: the Windows exe never repainted on resize).
func consoleSize() (w, h int) {
	w, h, err := term.GetSize(os.Stdout.Fd())
	if err != nil {
		return 0, 0
	}
	return w, h
}

// consoleWidth reports stdout's terminal width in columns (0 if unknown).
func consoleWidth() int {
	w, _ := consoleSize()
	return w
}
