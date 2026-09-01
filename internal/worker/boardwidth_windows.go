//go:build windows

package worker

import (
	"os"
	"syscall"
	"unsafe"
)

// consoleWidth reports stdout's console width in columns (0 if unknown).
func consoleWidth() int {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GetConsoleScreenBufferInfo")
	type coord struct{ X, Y int16 }
	type smallRect struct{ L, T, R, B int16 }
	type csbi struct {
		dwSize              coord
		dwCursorPosition    coord
		wAttributes         uint16
		srWindow            smallRect
		dwMaximumWindowSize coord
	}
	var info csbi
	r, _, _ := proc.Call(os.Stdout.Fd(), uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		return 0
	}
	return int(info.srWindow.R-info.srWindow.L) + 1
}
