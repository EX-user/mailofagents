package testbench

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"time"
)

// Env is the world one scenario run lives in: observer handle, run
// directory, config knobs. Scenarios must not reach past it (host
// isolation, annotation ③: all artifacts land under RunDir).
type Env struct {
	// Obs is the observer client (server + test account credentials).
	Obs *Obs
	// TL is this run's timeline — scenarios record evidence here.
	TL *Timeline
	// RunDir is this run's isolated artifact directory (timeline,
	// reports, later: worker logs, replay packs). Never a shared path.
	RunDir string
	// BenchRoot is the bench's isolated HOME/XDG root — spawned CLIs and
	// workers see this as HOME, never the host user's real profile.
	// Empty means RunDir's parent (set by the runner).
	BenchRoot string
	// WorkerBin is the path to the worker binary under test (optional:
	// only spawn scenarios need it).
	WorkerBin string
}

// Root returns the effective bench root.
func (e *Env) Root() string {
	if e.BenchRoot != "" {
		return e.BenchRoot
	}
	return filepath.Dir(e.RunDir)
}

// TimelinePath is where the run's timeline lives.
func (e *Env) TimelinePath() string { return filepath.Join(e.RunDir, "timeline.jsonl") }

// Run executes scenarios in the given order under per-scenario timeouts,
// each in its own run directory. It never panics through: a scenario
// error is a failed Result, not a crashed bench.
func Run(ctx context.Context, env *Env, scenarios []Scenario) []Result {
	results := make([]Result, 0, len(scenarios))
	for _, sc := range scenarios {
		results = append(results, runOne(ctx, env, sc))
	}
	return results
}

func runOne(ctx context.Context, env *Env, sc Scenario) Result {
	timeout := sc.Timeout()
	if timeout <= 0 {
		timeout = defaultScenarioTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	runDir := filepath.Join(env.RunDir, sanitize(sc.Name()))
	tl, err := OpenTimeline(filepath.Join(runDir, "timeline.jsonl"))
	if err != nil {
		res := Result{Scenario: sc.Name(), StartedAt: time.Now(), Duration: 0}
		res.add("timeline_open", false, "%v", err)
		return res
	}
	defer tl.Close()

	runEnv := &Env{Obs: env.Obs, TL: tl, RunDir: runDir, BenchRoot: env.BenchRoot, WorkerBin: env.WorkerBin}
	// swap in a timeline-aware observer copy so calls land on this run's
	// timeline (the shared env.Obs may carry a different handle).
	if env.Obs != nil {
		runEnv.Obs = &Obs{server: env.Obs.server, addr: env.Obs.addr, pass: env.Obs.pass, http: env.Obs.http, tl: tl}
	}
	_ = tl.Add("note", "scenario start", map[string]string{"name": sc.Name(), "desc": sc.Desc()})

	res := safeRun(runCtx, runEnv, sc)
	_ = tl.Add("note", "scenario end", map[string]any{"ok": res.OK, "assertions": len(res.Assertions)})
	return res
}

// safeRun converts panics into failed results — the bench must survive
// any single broken scenario.
func safeRun(ctx context.Context, env *Env, sc Scenario) (res Result) {
	res = Result{Scenario: sc.Name(), OK: true, StartedAt: time.Now()}
	defer func() {
		res.Duration = time.Since(res.StartedAt)
		if r := recover(); r != nil {
			res.add("panic", false, "scenario panicked: %v", r)
		}
		if ctx.Err() == context.DeadlineExceeded {
			res.add("timeout", false, "scenario exceeded its %s budget", sc.Timeout())
		}
	}()
	return sc.Run(ctx, env)
}

// sanitize keeps scenario names safe as directory names.
func sanitize(name string) string {
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

// Summary renders the console one-pager for a batch of results. batch
// (optional) adds the three-axis rubric line per scenario.
func Summary(results []Result, batch *BatchScore) string {
	sort.Slice(results, func(i, j int) bool { return results[i].Scenario < results[j].Scenario })
	s := ""
	for _, r := range results {
		mark := "PASS"
		if !r.OK {
			mark = "FAIL"
		}
		axes := ""
		if batch != nil {
			if ax := batch.PerScenario[r.Scenario]; len(ax) == 3 {
				axes = fmt.Sprintf("  [chain %d timing %d semantic %d]", ax[0].Score, ax[1].Score, ax[2].Score)
			}
		}
		s += fmt.Sprintf("%s  %-12s %d assertions, %s%s\n", mark, r.Scenario, len(r.Assertions), r.Duration.Round(time.Millisecond), axes)
		for _, a := range r.Assertions {
			if !a.OK {
				s += fmt.Sprintf("       ✗ %s: %s\n", a.Name, a.Detail)
			}
		}
	}
	if batch != nil && len(batch.AxisAverages) == 3 {
		s += fmt.Sprintf("batch axes: chain %d / timing %d / semantic %d (min %d/%d/%d)\n",
			batch.AxisAverages[0], batch.AxisAverages[1], batch.AxisAverages[2],
			batch.AxisMins[0], batch.AxisMins[1], batch.AxisMins[2])
	}
	return s
}
