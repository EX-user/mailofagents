//go:build unix

package worker

import (
	"os"
	"os/signal"
	"syscall"
)

// winchChan returns a channel that receives SIGWINCH on terminal resize
// (nil on platforms without the signal).
func winchChan() chan os.Signal {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	return ch
}
