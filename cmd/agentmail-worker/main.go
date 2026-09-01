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
	"syscall"

	"github.com/agentmail/agentmail/internal/worker"
)

func main() {
	cfgPath := flag.String("config", "worker.json", "path to worker config JSON")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[worker] ")

	cfg, err := worker.LoadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	worker.NewDuty(cfg).Run(ctx)
}
