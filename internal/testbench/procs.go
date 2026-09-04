package testbench

import (
	"os"
	"runtime"
)

func procEnvironPath(pid int) string {
	if runtime.GOOS == "windows" {
		// No /proc on Windows: the gate degrades to "cannot verify" and
		// the spawn scenario must report that honestly (M3+ Win item).
		return ""
	}
	return "/proc/" + itoa(pid) + "/environ"
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// ensureBenchFaces materializes the isolated config faces a spawned CLI
// expects under benchRoot (XDG dirs), so the CLI never falls back to the
// host user's real config paths.
func ensureBenchFaces(benchRoot string) error {
	for _, d := range []string{
		"/.config", "/.local/share", "/.local/state", "/.cache",
	} {
		if err := os.MkdirAll(benchRoot+d, 0o755); err != nil {
			return err
		}
	}
	return nil
}
