// agentmail-worker: MVP single-account duty worker (v4 phase-MVP).
//
// Watches one mailofagents account, wakes a CLI coding agent (pi first)
// with a mechanical digest of unread mail, and lets the agent answer with
// its own credentials. See internal/worker for the loop and adapter.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/agentmail/agentmail/internal/worker"
)

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// buildTag is injected via -ldflags at build time (release builds stamp the
// version; debug builds say so here). It prints once at startup so a running
// worker can always be identified from its first log line.
var buildTag = "unversioned"

func main() {
	cfgPath := flag.String("config", "worker.json", "path to worker config JSON")
	fresh := flag.Bool("fresh", false, "start a brand-new session: drop the stored session id and CLEAR the workdir contents (asks per account; see -yes)")
	yes := flag.Bool("yes", false, "assume yes for -fresh confirmations (for scripts; required when stdin is not a terminal)")
	agentSel := flag.String("switch_address", "", "only run the matching account (address prefix, local-part, or 1-based index); default runs all")
	showVer := flag.Bool("version", false, "print build tag and exit")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[worker] ")

	if *showVer {
		fmt.Println("agentmail-worker build:", buildTag)
		return
	}
	log.Printf("build: %s", buildTag)

	cfgs, err := worker.LoadConfigs(*cfgPath, *agentSel)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	// -fresh clears workdirs — destructive, so confirm per account unless
	// -yes. Non-interactive stdin without -yes refuses to run at all.
	freshList := make([]bool, len(cfgs))
	for i := range cfgs {
		freshList[i] = *fresh
	}
	if *fresh {
		if !*yes && !isTerminal(os.Stdin) {
			log.Fatalf("-fresh clears workdirs; refusing without -yes when stdin is not a terminal")
		}
		in := bufio.NewReader(os.Stdin)
		for i, c := range cfgs {
			if *yes {
				continue
			}
			fmt.Printf("Clear workdir %s for %s? [y/N] ", c.Workdir, c.Address)
			line, _ := in.ReadString('\n')
			line = strings.TrimSpace(strings.ToLower(line))
			if line != "y" && line != "yes" {
				freshList[i] = false
				fmt.Println("  kept (will resume existing session)")
			}
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// One independent duty loop per account — wakes are per-account state
	// machines (own session binding, own workdir) so they run in parallel
	// with no shared mutable state. SIGTERM cancels the shared context and
	// stops all of them. The status board redraws itself on a fast tick.
	go worker.RenderLoop(ctx)
	var wg sync.WaitGroup
	for i, cfg := range cfgs {
		wg.Add(1)
		go func(c *worker.Config, f bool) {
			defer wg.Done()
			worker.NewDuty(c, f).Run(ctx)
		}(cfg, freshList[i])
	}
	wg.Wait()
}
