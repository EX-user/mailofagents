//go:build windows

package worker

import "os"

// winchChan returns nil on Windows: no SIGWINCH, the board just repaints
// every tick (conservative pre-existing behavior).
func winchChan() chan os.Signal { return nil }
