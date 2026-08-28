// Command agentmail-gateway is the MCP stdio subprocess that an agent client
// spawns per session. It holds no persistent data; its only state is an
// in-memory access-code map that dies with the process. Every mailbox call is
// forwarded to agentmail-server over HTTP using credentials recovered from
// the access code.
//
// Usage (the agent client spawns this):
//
//	agentmail-gateway --server-url http://127.0.0.1:8090
//
// --server-url may also be set via AGENTMAIL_SERVER_URL. Optional tuning:
// --code-ttl (default 1h) and --code-max-calls (default 20).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/agentmail/agentmail/internal/gateway"
)

func main() {
	serverURL := flag.String("server-url", os.Getenv("AGENTMAIL_SERVER_URL"), "default agentmail-server origin, e.g. http://127.0.0.1:8090 (authenticate can target other servers via server_url)")
	codeTTL := flag.Duration("code-ttl", time.Hour, "access code lifetime")
	codeMaxCalls := flag.Int("code-max-calls", 20, "max tool calls per access code")
	flag.Parse()

	if *serverURL == "" {
		fmt.Fprintln(os.Stderr, "agentmail-gateway: --server-url or AGENTMAIL_SERVER_URL is required")
		os.Exit(2)
	}

	gateway.CodeTTL = *codeTTL
	gateway.CodeMaxCalls = *codeMaxCalls

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// MCP speaks JSON-RPC on stdio. Logs MUST go to stderr so they don't
	// corrupt the JSON stream on stdout.
	log.SetOutput(os.Stderr)

	srv := gateway.New(*serverURL)
	if err := srv.Serve(ctx); err != nil && ctx.Err() == nil {
		log.Printf("agentmail-gateway: %v", err)
		os.Exit(1)
	}
}
