// worker-testbench runs bench scenarios against a real server and the
// production worker binary, scoring behavior from mailbox-side evidence.
//
// M0: schema + observer + timeline + selfcheck (zero side effects).
// Later milestones add fault injection scenarios (S0-S5), the worker
// spawn harness, and the three-axis rubric reporter.
package main

import (
	"context"
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
	reportTo := flag.String("report-to", defaultReportTo(), "failure digest recipient (empty = no mail-out)")
	reportCC := flag.String("report-cc", defaultReportCC(), "failure digest cc (comma-separated)")
	flag.Parse()

	os.Exit(run(*server, *addr, *pass, *runsRoot, *workerBin, *scenarios, *timeout, *reportTo, *reportCC))
}

// Failure digest recipients are the whitepaper line (主测 Devi, cc alice);
// env overrides keep hosts from hardcoding addresses in cron units.
func defaultReportTo() string {
	if v := os.Getenv("TESTBENCH_REPORT_TO"); v != "" {
		return v
	}
	return "moa-dev-engineer@mailofagents.online"
}

func defaultReportCC() string {
	if v := os.Getenv("TESTBENCH_REPORT_CC"); v != "" {
		return v
	}
	return "moa-arch-engineer@mailofagents.online"
}

func defaultRunsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "runs"
	}
	return filepath.Join(home, "worker-testbench", "runs")
}

func run(server, addr, pass, runsRoot, workerBin, scenarioSel string, budget time.Duration, reportTo, reportCC string) int {
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

	batch := testbench.ScoreBatch(runDir, results)
	fmt.Print(testbench.Summary(results, batch))
	if err := testbench.WriteReport(runDir, results, batch); err != nil {
		fmt.Fprintf(os.Stderr, "testbench: report: %v\n", err)
		return 2
	}
	if _, err := testbench.BuildReplay(runDir, results, batch); err != nil {
		fmt.Fprintf(os.Stderr, "testbench: replay pack: %v\n", err)
		return 2
	}

	anyFail := false
	for _, r := range results {
		if !r.OK {
			anyFail = true
			break
		}
	}
	if anyFail {
		// 失败另存（annotation ④）：failed runs outlive the prune window,
		// and the failure digest mails out once per batch.
		if archived, err := testbench.ArchiveFailed(runsRoot, runDir); err == nil {
			runDir = archived
			fmt.Printf("failed run archived: %s\n", archived)
		}
		if reportTo != "" && addr != "" {
			cc := splitNames(reportCC)
			if err := testbench.MailFailure(server, addr, pass, []string{reportTo}, cc, results, runDir); err != nil {
				fmt.Fprintf(os.Stderr, "testbench: failure mail: %v\n", err)
			}
		}
	}
	if pruned, err := testbench.PruneRuns(runsRoot, testbench.RetentionWindow, time.Now()); err == nil && len(pruned) > 0 {
		fmt.Printf("pruned %d run(s) past the %s window\n", len(pruned), testbench.RetentionWindow)
	}
	if anyFail {
		return 1
	}
	return 0
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
