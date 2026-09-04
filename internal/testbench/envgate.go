package testbench

import (
	"os"
	"strings"
)

// WhitelistEnv builds a child-process environment from an allowlist —
// the bench never passes os.Environ() through, so production secrets
// (API keys et al.) cannot leak into spawned workers by inheritance
// (boss directive, M1 hard gate).
//
// HOME is repointed at benchRoot so CLIs read the bench's own config
// faces (XDG fallback), never the host user's real one.
func WhitelistEnv(benchRoot string, extra ...string) []string {
	env := []string{
		"HOME=" + benchRoot,
		"XDG_CONFIG_HOME=" + benchRoot + "/.config",
		"XDG_DATA_HOME=" + benchRoot + "/.local/share",
		"XDG_STATE_HOME=" + benchRoot + "/.local/state",
		"XDG_CACHE_HOME=" + benchRoot + "/.cache",
		"LANG=C.UTF-8",
	}
	if path := os.Getenv("PATH"); path != "" {
		env = append(env, "PATH="+path)
	}
	if term := os.Getenv("TERM"); term != "" {
		env = append(env, "TERM="+term)
	}
	return append(env, extra...)
}

// secretPatterns are the variable-name prefixes/values that must never
// appear in a spawned bench process's environment. Name-based: we assert
// on keys, not values — values may legitimately contain the word "key".
var secretPatterns = []string{
	"ANTHROPIC_", "DEEPSEEK_", "OPENAI_", "API_KEY", "TOKEN=",
	"SECRET", "PASSWORD",
}

// EnvironScan reports every environment entry whose NAME matches a
// secret pattern. Scanning names (not values) keeps the evidence itself
// free of secrets — the report can be shipped as-is.
func EnvironScan(environ []string) (leaks []string) {
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		upper := strings.ToUpper(name)
		for _, p := range secretPatterns {
			if strings.Contains(upper, p) {
				leaks = append(leaks, name)
				break
			}
		}
	}
	return leaks
}

// ReadProcEnviron reads /proc/<pid>/environ (NUL-separated). Works only
// while the process is alive and only for same-uid processes — exactly
// the bench's window and scope.
func ReadProcEnviron(pid int) ([]string, error) {
	raw, err := os.ReadFile(procEnvironPath(pid))
	if err != nil {
		return nil, err
	}
	parts := strings.Split(string(raw), "\x00")
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out, nil
}
