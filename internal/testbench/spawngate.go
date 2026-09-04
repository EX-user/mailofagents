package testbench

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// spawnGate is the M1 hard gate (boss directive): prove that the bench
// spawns worker processes with a whitelisted environment — no production
// secret can ride into the child tree by inheritance.
//
// M1a form: spawn `worker -version` under WhitelistEnv and scan
// /proc/<pid>/environ while it lives. A -version process exits fast, so
// the scan is best-effort: if it cannot be read in time the gate says
// "unverifiable" explicitly instead of passing silently — construction
// (WhitelistEnv is the only env source) plus the S1 live-poll variant
// close that gap.
type spawnGate struct{}

func (spawnGate) Name() string { return "spawn-env-gate" }
func (spawnGate) Desc() string {
	return "spawn 白名单环境硬门禁：worker 子进程环境零生产密钥（/proc 扫描实证）"
}
func (spawnGate) Timeout() time.Duration { return time.Minute }

func (spawnGate) Run(ctx context.Context, env *Env) Result {
	res := Result{Scenario: "spawn-env-gate", OK: true, StartedAt: time.Now()}
	defer func() { res.Duration = time.Since(res.StartedAt) }()

	if env.WorkerBin == "" {
		res.add("worker_bin_configured", false, "env.WorkerBin is empty — spawn scenarios need a binary path")
		return res
	}
	res.add("worker_bin_configured", true, "%s", env.WorkerBin)

	root := env.Root()
	if err := ensureBenchFaces(root); err != nil {
		res.add("bench_faces", false, "%v", err)
		return res
	}

	// CommandContext: on scenario-budget timeout the child gets the
	// context kill after WaitDelay escalation — no bare process leaks.
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, env.WorkerBin, "-version")
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = WhitelistEnv(root)
	cmd.Dir = root
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out

	if err := cmd.Start(); err != nil {
		res.add("spawn", false, "%v", err)
		return res
	}
	res.add("spawn", true, "pid=%d", cmd.Process.Pid)
	_ = env.TL.Add("note", "worker spawned", map[string]any{"pid": cmd.Process.Pid, "bin": env.WorkerBin, "env_source": "whitelist"})

	// Scan while alive; retry briefly — -version exits in milliseconds.
	leaks, scanned := []string(nil), false
	for i := 0; i < 20 && !scanned; i++ {
		environ, err := ReadProcEnviron(cmd.Process.Pid)
		if err == nil {
			leaks, scanned = EnvironScan(environ), true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	switch {
	case scanned && len(leaks) == 0:
		res.add("environ_zero_secrets", true, "/proc scan clean (name-based patterns: %d)", len(secretPatterns))
		_ = env.TL.Add("assert", "environ_zero_secrets", map[string]any{"ok": true, "scanned": true, "probe": "worker -version"})
	case scanned:
		res.add("environ_zero_secrets", false, "secret-named vars leaked: %v", leaks)
		_ = env.TL.Add("assert", "environ_zero_secrets", map[string]any{"ok": false, "leaks": leaks})
	default:
		// The -version child exited before the scan landed. Same env
		// source, longer-lived probe child: scan that, and say so.
		ok2, detail := scanProbeChild(root)
		res.add("environ_zero_secrets", ok2, "%s", detail)
		_ = env.TL.Add("assert", "environ_zero_secrets", map[string]any{"ok": ok2, "probe": "sleep probe (same WhitelistEnv)", "detail": detail})
	}

	if err := cmd.Wait(); err != nil {
		res.add("exit", false, "%v", err)
		return res
	}
	res.add("exit", true, "code 0")

	if !strings.Contains(out.String(), "agentmail-worker build:") {
		res.add("version_output", false, "output missing build line: %q", truncate(out.String(), 120))
		return res
	}
	res.add("version_output", true, "%s", firstLine(out.String()))
	return res
}

// scanProbeChild spawns a deliberately slow child (sleep) under the same
// WhitelistEnv and scans its /proc environ — closing the race the fast
// -version child opens. Unix only; Windows degrades to unverifiable
// (M3+ item, per the Win appendix).
func scanProbeChild(root string) (bool, string) {
	if runtime.GOOS == "windows" {
		return false, "UNVERIFIABLE on windows (no /proc) — Win acceptance item, M3+"
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		return false, "UNVERIFIABLE: no sleep binary for probe child"
	}
	probe := exec.Command(sleep, "0.6")
	probe.Env = WhitelistEnv(root)
	probe.Dir = root
	if err := probe.Start(); err != nil {
		return false, fmt.Sprintf("UNVERIFIABLE: probe spawn: %v", err)
	}
	defer probe.Process.Kill()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		environ, err := ReadProcEnviron(probe.Process.Pid)
		if err == nil {
			if leaks := EnvironScan(environ); len(leaks) > 0 {
				return false, fmt.Sprintf("secret-named vars leaked: %v", leaks)
			}
			return true, fmt.Sprintf("/proc scan clean via probe child (patterns: %d)", len(secretPatterns))
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false, "UNVERIFIABLE: probe child never readable"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func init() { register(spawnGate{}) }
