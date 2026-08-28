// Package gateway is the agentmail MCP gateway. It is a stdio subprocess that
// an agent client spawns per session. It holds NO persistent data — its only
// state is an in-memory map of access codes to credentials, which dies with
// the process. Every mailbox operation is forwarded to agentmail-server over
// HTTP using the credentials recovered from the access code.
//
// Lifecycle: the agent client spawns this binary once per session; it serves
// JSON-RPC on stdin/stdout until the pipe closes (session end), then exits.
// One subprocess == one session. When it exits, all its access codes vanish.
package gateway

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/agentmail/agentmail/internal/httpclient"
)

// CodeTTL is how long an access code stays valid.
var CodeTTL = time.Hour

// Version is the agentmail-gateway version. Overridden at build time via
// -ldflags "-X github.com/agentmail/agentmail/internal/gateway.Version=v0.1.2".
// Reported in the MCP initialize handshake and via server_info(query="status")
// alongside the server's suggested_min_gateway_version, so agents and ops can
// tell whether the gateway binary is due for a swap.
var Version = "dev"

// CodeMaxCalls is how many tool calls one access code may serve.
var CodeMaxCalls = 20

// Server is the gateway: MCP transport + access-code map + multi-server HTTP clients.
// The gateway can talk to multiple agentmail-server instances. The default
// server is set at startup (--server-url); authenticate can target a different
// server by passing server_url. Each access code remembers which server it
// belongs to, so subsequent calls route automatically.
type Server struct {
	defaultURL string
	clients    map[string]*httpclient.Client // serverURL -> cached client

	mu    sync.Mutex
	codes map[string]*codeEntry // access_code plaintext -> entry

	in  *bufio.Reader
	out io.Writer
}

// codeEntry is the in-memory record for one access code.
type codeEntry struct {
	Address   string
	Password  string
	ServerURL string // which server this code was authenticated against
	ExpiresAt time.Time
	CallsUsed int
	MaxCalls  int
}

// New returns a gateway whose default server is baseURL, reading MCP from
// stdin and writing to stdout.
func New(baseURL string) *Server {
	c := httpclient.New(baseURL)
	return &Server{
		defaultURL: c.BaseURL(), // normalized (trailing slash trimmed)
		clients:    map[string]*httpclient.Client{c.BaseURL(): c},
		codes:      make(map[string]*codeEntry),
		in:         bufio.NewReader(os.Stdin),
		out:        os.Stdout,
	}
}

// getClient returns the HTTP client for serverURL, creating and caching it on
// first use. If serverURL is empty, the default server is used.
func (s *Server) getClient(serverURL string) *httpclient.Client {
	if serverURL == "" {
		serverURL = s.defaultURL
	}
	serverURL = strings.TrimRight(serverURL, "/")
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.clients[serverURL]; ok {
		return c
	}
	c := httpclient.New(serverURL)
	s.clients[serverURL] = c
	return c
}

// Serve runs the JSON-RPC loop until stdin closes.
func (s *Server) Serve(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line, err := s.in.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if isEmpty(line) {
			continue
		}
		s.handle(ctx, line)
	}
}

// --- access code management (gateway-local, in-memory) ---

// issueCode verifies credentials with the specified server (or the default if
// serverURL is empty) and, on success, mints a short-lived access code bound
// to them. The code remembers which server it was authenticated against.
func (s *Server) issueCode(ctx context.Context, address, password, serverURL string) (string, error) {
	client := s.getClient(serverURL)
	if err := client.VerifyPassword(address, password); err != nil {
		return "", fmt.Errorf("authentication failed: %w", err)
	}
	code, err := randomCode(32)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.codes[code] = &codeEntry{
		Address:   address,
		Password:  password,
		ServerURL: client.BaseURL(),
		ExpiresAt: time.Now().Add(CodeTTL),
		CallsUsed: 0,
		MaxCalls:  CodeMaxCalls,
	}
	s.mu.Unlock()
	return code, nil
}

// consumeCode validates a code, returns the entry, and consumes one call
// against the per-code call budget. Used for operations with side effects
// (register, authenticate, send_email). Returns ErrInvalidCode if the code is
// unknown, expired, or exhausted.
func (s *Server) consumeCode(code string) (*codeEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.codes[code]
	if !ok {
		return nil, ErrInvalidCode
	}
	if time.Now().After(e.ExpiresAt) {
		delete(s.codes, code)
		return nil, ErrInvalidCode
	}
	if e.CallsUsed >= e.MaxCalls {
		delete(s.codes, code)
		return nil, ErrInvalidCode
	}
	e.CallsUsed++
	return e, nil
}

// consumeCodeReadOnly validates a code and returns the entry WITHOUT consuming
// a call against the budget. Used for read-only operations (read_inbox,
// get_message, wait_for_new_mail). The TTL still applies.
func (s *Server) consumeCodeReadOnly(code string) (*codeEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.codes[code]
	if !ok {
		return nil, ErrInvalidCode
	}
	if time.Now().After(e.ExpiresAt) {
		delete(s.codes, code)
		return nil, ErrInvalidCode
	}
	return e, nil
}

// ErrInvalidCode signals an unknown, expired, or exhausted access code.
var ErrInvalidCode = errors.New("invalid or expired access code")

// randomCode returns a hex-encoded random string of n bytes.
func randomCode(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func isEmpty(line []byte) bool {
	for _, b := range line {
		if b != ' ' && b != '\t' && b != '\n' && b != '\r' {
			return false
		}
	}
	return true
}

// --- MCP JSON-RPC transport ---

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	codeParseError     = -32700
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

func (s *Server) handle(ctx context.Context, raw []byte) {
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		s.respond(rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: codeParseError, Message: "parse error"}})
		return
	}
	isNotification := len(req.ID) == 0
	switch req.Method {
	case "initialize":
		s.respond(s.handleInitialize(req))
	case "notifications/initialized":
		// no-op; notifications get no reply
	case "tools/list":
		s.respond(s.handleToolsList(req))
	case "tools/call":
		s.respond(s.handleToolsCall(ctx, req))
	case "ping":
		s.respond(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
	default:
		if !isNotification {
			s.respond(rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: codeMethodNotFound, Message: "method not found: " + req.Method}})
		}
	}
}

func (s *Server) handleInitialize(req rpcRequest) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "agentmail", "version": Version},
		"instructions":    agentmailInstructions,
	}}
}

// agentmailInstructions is the server-level guidance returned during the MCP
// initialize handshake. Clients surface it to the agent so a fresh session
// knows what agentmail is and the typical flow, without having to infer it
// from individual tool descriptions.
const agentmailInstructions = `agentmail is a local mail system that lets AI agents exchange messages using persistent identities (name@agentmail.local). You operate as one account.

Typical flow:
1. If you already have credentials (address + password), call authenticate to get an access_code.
2. Use the access_code with send_email / read_inbox / get_message / wait_for_new_mail.
3. The access_code is session-scoped: it expires after ~1h (TTL) or 20 write-side calls. Reads (read_inbox / get_message / wait_for_new_mail) do NOT count against the budget, so you can poll freely.
4. If an access_code stops working, call authenticate again with the same credentials to mint a new one.

Two integration paths — pick the one your environment gives you:
- MCP (preferred): spawn the gateway binary as a stdio MCP server. On Windows the binary is agentmail-gateway.exe (the .exe suffix is required); on Linux/macOS it is ./agentmail-gateway. Prefer the args form (agentmail-gateway --server-url https://your-server); the AGENTMAIL_SERVER_URL environment variable is also accepted if args are awkward.
- HTTP fallback: every account-scoped endpoint also works with plain HTTP Basic auth (address:password) — e.g. POST /api/send, GET /api/inbox. If the MCP tools are not active in your session (no authenticate tool available), go straight to HTTP; authenticate exists only on the MCP side.

Multiple servers: this gateway can talk to more than one agentmail server. Pass server_url (e.g. http://10.0.0.5:8090) to authenticate against a different server than the default. The access code remembers which server it belongs to, so send_email/read_inbox/get_message/wait_for_new_mail route automatically — no need to pass server_url on every call.

If you do NOT have credentials, you can either call register(name) to create an account yourself (the endpoint is open; pick a clear ASCII name like "frontend-engineer-1"), or ask the admin to register one for you. Admin registration is preferred in shared/production environments to avoid account sprawl; self-registration is fine for personal/testing use.

Operating mode:
agentmail is a tool you pick up when needed — like any other tool. Most sessions use it occasionally: send a message, check for a reply, move on. Do NOT autonomously enter a polling/watch loop. Only watch the inbox continuously when the user has explicitly asked you to do so. Otherwise, do your current task and stop normally; the mailbox will still be there next time you need it.

Read vs unread:
- read_inbox returns a list with an "unread" flag on each message. It does NOT change read state.
- Only get_message marks a message as read (and returns the full body). If you skip get_message, the unread indicator stays on forever, even after you've seen the preview in read_inbox.
- So when you spot an unread message in read_inbox that you intend to handle, always call get_message on it — both to read the full body and to clear the unread flag.

Identity & directory:
- Your account has a directory profile: whether it is "listed" (visible) in the public address book, and a short signature shown next to your address.
- account_info(query="self") shows your own profile; account_info(query="directory") shows every listed account. Use these — not server_info — for account-level queries.
- update_profile(visible, signature) changes your own profile (e.g. opt into the directory, set a tagline so others know who you are). Signature is capped at 200 chars.
- Setting a short, human-readable signature (e.g. a name and role) makes collaboration easier — others can look you up in the directory.

When you ARE watching (user asked you to):
- Call duty_watch_guide (no arguments) for a concise text guide on the two watching modes (MCP polling vs script polling) with a ready-to-use script.

Worked example (two agents exchanging mail):
  authenticate(address="alice@agentmail.local", password="...")  -> access_code
  send_email(access_code, to="bob@agentmail.local", subject="hi", body="hello")
  read_inbox(access_code, limit=10)                                -> see replies (unread=true)
  get_message(access_code, message_id="...")                       -> full body, marks read`

func (s *Server) respond(resp rpcResponse) {
	if len(resp.ID) == 0 {
		return // notification
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintln(s.out, string(out))
}
