// worker-testbench runs bench scenarios against a real server and the
// production worker binary, scoring behavior from mailbox-side evidence.
//
// M0: schema + observer + timeline + selfcheck (zero side effects).
// Later milestones add fault injection scenarios (S0-S5), the worker
// spawn harness, and the three-axis rubric reporter.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/agentmail/agentmail/internal/testbench"
)

func main() {
	server := flag.String("server", "https://mailofagents.online", "server base URL")
	addr := flag.String("address", os.Getenv("TESTBENCH_ADDRESS"), "observed test account address (empty = public endpoints only)")
	pass := flag.String("password", os.Getenv("TESTBENCH_PASSWORD"), "observed test account password")
	runsRoot := flag.String("runs-dir", defaultRunsDir(), "root directory for run artifacts (host isolation: everything lands under here)")
	workerBin := flag.String("worker-bin", os.Getenv("TESTBENCH_WORKER_BIN"), "worker binary under test (spawn scenarios)")
	scenarios := flag.String("scenarios", "selfcheck", "comma-separated scenario names, or \"all\"")
	timeout := flag.Duration("timeout", 2*time.Hour, "whole-bench budget")
	flag.Parse()

	os.Exit(run(*server, *addr, *pass, *runsRoot, *workerBin, *scenarios, *timeout))
}

func defaultRunsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "runs"
	}
	return filepath.Join(home, "worker-testbench", "runs")
}

func run(server, addr, pass, runsRoot, workerBin, scenarioSel string, budget time.Duration) int {
	runDir := filepath.Join(runsRoot, time.Now().Format("20060102-150405"))
	tl, err := testbench.OpenTimeline(filepath.Join(runDir, "bench.jsonl"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "testbench: %v\n", err)
		return 2
	}
	defer tl.Close()

	obs := testbench.NewObs(server, addr, pass, tl)
	env := &testbench.Env{Obs: obs, RunDir: runDir, BenchRoot: filepath.Dir(runsRoot), WorkerBin: workerBin}

	names := testbench.All()
	if scenarioSel != "all" {
		names = splitNames(scenarioSel)
	}
	var scenarios []testbench.Scenario
	for _, n := range names {
		sc, ok := testbench.Lookup(n)
		if !ok {
			fmt.Fprintf(os.Stderr, "testbench: unknown scenario %q (known: %v)\n", n, sortedNames())
			return 2
		}
		scenarios = append(scenarios, sc)
	}

	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	results := testbench.Run(ctx, env, scenarios)

	fmt.Print(testbench.Summary(results))
	if err := writeReport(runDir, results); err != nil {
		fmt.Fprintf(os.Stderr, "testbench: report: %v\n", err)
		return 2
	}
	for _, r := range results {
		if !r.OK {
			return 1
		}
	}
	return 0
}

func writeReport(runDir string, results []testbench.Result) error {
	type report struct {
		GeneratedAt time.Time          `json:"generated_at"`
		Results     []testbench.Result `json:"results"`
	}
	body, err := json.MarshalIndent(report{GeneratedAt: time.Now(), Results: results}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(runDir, "report.json"), body, 0o644)
}

func splitNames(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		switch r {
		case ',', ' ', '\t':
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
		default:
			cur += string(r)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func sortedNames() []string {
	names := testbench.All()
	sort.Strings(names)
	return names
}
