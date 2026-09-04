package testbench

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fixtureServer is a minimal stand-in for the public API contract —
// enough for observer-level scenarios; no internal knowledge anywhere.
func fixtureServer(t *testing.T, service string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/info", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"domain": service, "initialized": true, "query": "status",
			"suggested_min_gateway_version": "v0", "version": "vTEST",
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func runDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

func TestSelfcheckEndToEnd(t *testing.T) {
	srv := fixtureServer(t, "mailofagents.test")

	tl, err := OpenTimeline(filepath.Join(runDir(t), "t.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer tl.Close()

	env := &Env{Obs: NewObs(srv.URL, "", "", tl), RunDir: runDir(t)}
	sc, _ := Lookup("selfcheck")
	res := Run(context.Background(), env, []Scenario{sc})[0]

	if !res.OK {
		t.Fatalf("selfcheck failed: %+v", res.Assertions)
	}
	if len(res.Assertions) != 3 {
		t.Fatalf("want 3 assertions, got %d", len(res.Assertions))
	}

	// timeline evidence must exist and carry both calls
	raw, err := os.ReadFile(filepath.Join(env.RunDir, "selfcheck", "timeline.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if n := countMatches(raw, []byte(`"kind":"call"`)); n != 1 {
		t.Errorf("timeline calls = %d, want 1 (info call)\n%s", n, raw)
	}
	if n := countMatches(raw, []byte(`"kind":"note"`)); n != 2 {
		t.Errorf("timeline start/end notes = %d, want 2", n)
	}
}

func TestSelfcheckAgainstWrongService(t *testing.T) {
	// wrong-service fixture: initialized=false must fail the identity check.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"domain": "x", "initialized": false, "version": ""})
	}))
	t.Cleanup(bad.Close)
	srv := bad

	tl, _ := OpenTimeline(filepath.Join(runDir(t), "t.jsonl"))
	defer tl.Close()

	env := &Env{Obs: NewObs(srv.URL, "", "", tl), RunDir: runDir(t)}
	sc, _ := Lookup("selfcheck")
	res := safeRun(context.Background(), env, sc)

	if res.OK {
		t.Fatal("selfcheck must fail against a wrong service identity")
	}
	found := 0
	for _, a := range res.Assertions {
		if !a.OK && (a.Name == "service_initialized" || a.Name == "version_present") {
			found++
		}
	}
	if found == 0 {
		t.Fatalf("identity assertions missing/ok: %+v", res.Assertions)
	}
}

func TestScenarioTimeoutReported(t *testing.T) {
	slow := namedScenario{name: "slow", timeout: 30 * time.Millisecond, run: func(ctx context.Context, env *Env) Result {
		res := Result{Scenario: "slow", OK: true, StartedAt: time.Now()}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
		return res
	}}

	env := &Env{Obs: NewObs("http://127.0.0.1:1", "", "", mustTL(t)), RunDir: runDir(t)}
	res := Run(context.Background(), env, []Scenario{slow})[0]
	if res.OK {
		t.Fatal("slow scenario must fail via timeout assertion")
	}
	found := false
	for _, a := range res.Assertions {
		if a.Name == "timeout" && !a.OK {
			found = true
		}
	}
	if !found {
		t.Fatalf("timeout assertion missing: %+v", res.Assertions)
	}
}

func TestSanitize(t *testing.T) {
	if got := sanitize("a b/c.d"); got != "a_b_c_d" {
		t.Errorf("sanitize = %q", got)
	}
}

// namedScenario lets table tests define one-off scenarios.
type namedScenario struct {
	name    string
	desc    string
	timeout time.Duration
	run     func(context.Context, *Env) Result
}

func (n namedScenario) Name() string                             { return n.name }
func (n namedScenario) Desc() string                             { return n.desc }
func (n namedScenario) Timeout() time.Duration                   { return n.timeout }
func (n namedScenario) Run(ctx context.Context, env *Env) Result { return n.run(ctx, env) }

func mustTL(t *testing.T) *Timeline {
	t.Helper()
	tl, err := OpenTimeline(filepath.Join(runDir(t), "t.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tl.Close() })
	return tl
}

func countMatches(b, sub []byte) int {
	n := 0
	for i := 0; i+len(sub) <= len(b); {
		j := indexOf(b[i:], sub)
		if j < 0 {
			break
		}
		n++
		i += j + len(sub)
	}
	return n
}

func indexOf(b, sub []byte) int {
	for i := 0; i+len(sub) <= len(b); i++ {
		match := true
		for j := range sub {
			if b[i+j] != sub[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
