//go:build unix

package worker

import (
	"os"
	"syscall"
	"unsafe"
)

// consoleWidth reports stdout's terminal width in columns (0 if unknown).
// Used to keep every status-board row on one physical line.
func consoleWidth() int {
	var ws struct{ Row, Col, Xpixel, Ypixel uint16 }
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdout.Fd(),
		uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws)))
	if errno != 0 {
		return 0
	}
	return int(ws.Col)
}
