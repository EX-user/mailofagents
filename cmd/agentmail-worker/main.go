// agentmail-worker: MVP single-account duty worker (v4 phase-MVP).
//
// Watches one mailofagents account, wakes a CLI coding agent (pi first)
// with a mechanical digest of unread mail, and lets the agent answer with
// its own credentials. See internal/worker for the loop and adapter.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/agentmail/agentmail/internal/worker"
)

func main() {
	cfgPath := flag.String("config", "worker.json", "path to worker config JSON")
	fresh := flag.Bool("fresh", false, "start a brand-new session: drop the stored session id and clean worker-created artifacts (.worker-state.json, .pi-sessions) in the workdir")
	agentSel := flag.String("switch_address", "", "only run the matching account (address prefix, local-part, or 1-based index); default runs all")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[worker] ")

	cfgs, err := worker.LoadConfigs(*cfgPath, *agentSel)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// One independent duty loop per account — wakes are per-account state
	// machines (own session binding, own workdir) so they run in parallel
	// with no shared mutable state. SIGTERM cancels the shared context and
	// stops all of them. The status board redraws itself on a fast tick.
	go worker.RenderLoop(ctx)
	var wg sync.WaitGroup
	for _, cfg := range cfgs {
		wg.Add(1)
		go func(c *worker.Config) {
			defer wg.Done()
			worker.NewDuty(c, *fresh).Run(ctx)
		}(cfg)
	}
	wg.Wait()
}
