// Command agentmail-server is the persistent message store and HTTP API.
//
// It owns the bbolt database and serves all mailbox operations behind HTTP
// Basic auth. It has no concept of access codes or sessions — that lives in
// the gateway. One server process serves many agent sessions concurrently.
//
// Usage:
//
//	agentmail-server                     # auto: wizard if uninitialized, else serve
//	agentmail-server --init              # force browser wizard
//	agentmail-server --yes-init-from-config  # unattended init from TOML
//	agentmail-server --config path/to/agentmail.toml
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/agentmail/agentmail/internal/audit"
	"github.com/agentmail/agentmail/internal/config"
	"github.com/agentmail/agentmail/internal/server"
	"github.com/agentmail/agentmail/internal/store"
)

func main() {
	configPath := flag.String("config", config.DefaultConfigPath(), "path to agentmail.toml")
	initFlag := flag.Bool("init", false, "force the browser setup wizard")
	yesFlag := flag.Bool("yes-init-from-config", false, "unattended init from TOML (requires domain + admin.password in config)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// --- decide which path to take ---

	switch {
	case *yesFlag:
		// Unattended init from config file.
		runYesInit(cfg, ctx)
	case *initFlag:
		// Force browser wizard. Reject if already initialized.
		runForcedWizard(cfg, ctx)
	default:
		// Auto: serve if db is ready, else wizard.
		runAuto(cfg, ctx)
	}
}

// runYesInit does unattended init from the TOML config file.
func runYesInit(cfg *config.Config, ctx context.Context) {
	if !cfg.HasInitConfig() {
		fatal("--yes-init-from-config requires [server].domain and [admin].password in the config file")
	}
	st, err := store.Open(cfg.Storage.DBPath)
	if err != nil {
		fatal("open store: %v", err)
	}
	if st.IsInitialized() {
		fatal("already initialized")
	}
	if err := st.BootstrapSystem("admin", cfg.Admin.Password, cfg.Server.Domain); err != nil {
		fatal("bootstrap: %v", err)
	}
	if err := st.SetListen(cfg.Server.Listen); err != nil {
		fatal("set listen: %v", err)
	}
	log.Printf("agentmail-server: initialized via config (domain %s)", cfg.Server.Domain)
	serve(st, cfg, ctx)
}

// runForcedWizard forces the browser wizard.
func runForcedWizard(cfg *config.Config, ctx context.Context) {
	// If db exists and is initialized, refuse.
	if fileExists(cfg.Storage.DBPath) {
		st, err := store.Open(cfg.Storage.DBPath)
		if err != nil {
			fatal("open store: %v", err)
		}
		if st.IsInitialized() {
			st.Close()
			fatal("already initialized — delete %s to reconfigure", cfg.Storage.DBPath)
		}
		st.Close()
	}
	result, err := server.RunWizard(cfg)
	if err != nil {
		fatal("wizard: %v", err)
	}
	serve(result.Store, cfg, ctx)
}

// runAuto serves if the db exists and is initialized, else launches the wizard.
func runAuto(cfg *config.Config, ctx context.Context) {
	if fileExists(cfg.Storage.DBPath) {
		st, err := store.Open(cfg.Storage.DBPath)
		if err != nil {
			fatal("open store: %v", err)
		}
		if st.IsInitialized() {
			// Normal startup. Use bbolt listen if set, else config.
			listen := st.GetListen()
			if listen != "" {
				cfg.Server.Listen = listen
			}
			serve(st, cfg, ctx)
			return
		}
		// db exists but not initialized — fall through to wizard.
		st.Close()
	}
	// db doesn't exist or not initialized → wizard.
	result, err := server.RunWizard(cfg)
	if err != nil {
		fatal("wizard: %v", err)
	}
	serve(result.Store, cfg, ctx)
}

// serve starts the real HTTP server with the given store and config.
// If the store has a listen address in bbolt (set by the wizard), it
// overrides cfg.Server.Listen.
func serve(st *store.Store, cfg *config.Config, ctx context.Context) {
	if l := st.GetListen(); l != "" {
		cfg.Server.Listen = l
	}
	auditStore, err := audit.New(st.DB())
	if err != nil {
		fatal("init audit: %v", err)
	}
	srv := server.New(st, auditStore, cfg)
	log.Printf("agentmail-server listening on %s (domain %s)", cfg.Server.Listen, st.GetDomain())
	if err := srv.ListenAndServe(ctx); err != nil && ctx.Err() == nil {
		log.Printf("agentmail-server: %v", err)
		os.Exit(1)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "agentmail-server: "+format+"\n", args...)
	os.Exit(1)
}
