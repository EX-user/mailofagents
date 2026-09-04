// Package testbench is the worker test bench: a standalone, scheduled
// process that runs declarative scenarios against a real (or fixture)
// mail server and a real worker binary, then scores behavior from
// timeline evidence — not a green/red shell.
//
// Design contract (bench whitepaper v1 + annotations, boss-approved
// 2026-09-04):
//   - scenario: declarative description of a real operation sequence
//     (who/when/what/expect), each runnable independently;
//   - observer: mailbox-side assertions only, strictly against public
//     API contract fields — no session poking, no internal knowledge;
//   - runner: standalone process (systemd timer / cron), host-isolated;
//   - reporter: three-axis rubric + replay pack (M3); failures mail out.
//
// M0 skeleton: schema + observer client + timeline persistence + one
// end-to-end runnable scenario (selfcheck) + minimal report.
package testbench

import (
	"context"
	"fmt"
	"time"
)

// Scenario is one runnable剧本: a named, independent description of an
// operation sequence with its expectations. Implementations must be
// safe to run under ctx cancellation (the runner enforces Timeout).
type Scenario interface {
	Name() string
	Desc() string
	// Timeout bounds one run; zero means the runner default.
	Timeout() time.Duration
	// Run executes the scenario against env and returns its result.
	Run(ctx context.Context, env *Env) Result
}

// Result carries what a scenario observed and concluded. Assertions are
// recorded as they are evaluated so a failed run still ships a full
// timeline-backed account.
type Result struct {
	Scenario   string        `json:"scenario"`
	OK         bool          `json:"ok"`
	StartedAt  time.Time     `json:"started_at"`
	Duration   time.Duration `json:"duration"`
	Assertions []Assertion   `json:"assertions"`
	Notes      []string      `json:"notes,omitempty"`
}

// Assertion is one check with its evidence trail.
type Assertion struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	// Detail is human-readable evidence: what was expected vs seen.
	Detail string `json:"detail"`
}

func (r *Result) add(name string, ok bool, format string, args ...any) {
	r.Assertions = append(r.Assertions, Assertion{Name: name, OK: ok, Detail: fmt.Sprintf(format, args...)})
	if !ok {
		r.OK = false
	}
}

// expect records an assertion: want must equal got.
func (r *Result) expect(name string, want, got any) {
	r.add(name, want == got, "want %v, got %v", want, got)
}

// Runner default for scenarios that do not set Timeout.
const defaultScenarioTimeout = 2 * time.Hour

// registry holds every built-in scenario; main picks from it by name.
var registry = map[string]Scenario{}

func register(s Scenario) { registry[s.Name()] = s }

func init() { register(selfcheck{}) }

// All returns every registered scenario name (sorted by caller).
func All() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	return names
}

// Lookup fetches a scenario by name.
func Lookup(name string) (Scenario, bool) {
	s, ok := registry[name]
	return s, ok
}

// selfcheck is the M0 connectivity scenario: public /api/info must
// answer with the expected service identity. Zero side effects — no
// account, no mail, no worker spawn — so it is safe on any host.
type selfcheck struct{}

func (selfcheck) Name() string { return "selfcheck" }
func (selfcheck) Desc() string {
	return "公开 /api/info 连通性与服务身份断言（零副作用）"
}
func (selfcheck) Timeout() time.Duration { return time.Minute }

func (selfcheck) Run(ctx context.Context, env *Env) Result {
	res := Result{Scenario: "selfcheck", OK: true, StartedAt: time.Now()}
	defer func() { res.Duration = time.Since(res.StartedAt) }()

	info, err := env.Obs.Info(ctx)
	if err != nil {
		res.add("info_reachable", false, "GET /api/info: %v", err)
		return res
	}
	res.add("info_reachable", true, "domain=%q version=%q initialized=%v", info.Domain, info.Version, info.Initialized)
	res.add("service_initialized", info.Initialized, "initialized=%v", info.Initialized)
	res.add("version_present", info.Version != "", "version=%q", info.Version)
	return res
}
