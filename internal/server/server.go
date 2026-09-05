// Package server is agentmail-server's HTTP API. It exposes the message-store
// operations behind HTTP Basic auth: every mailbox-affecting endpoint
// authenticates as the acting account, so per-account isolation is enforced
// by the server (not by the gateway or by convention). The gateway holds no
// data and simply forwards Basic-authed requests.
package server

import (
	"context"
	"encoding/base64"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/agentmail/agentmail/internal/audit"
	"github.com/agentmail/agentmail/internal/config"
	"github.com/agentmail/agentmail/internal/store"
)

// Version is the agentmail-server version. Overridden at build time via
// -ldflags "-X github.com/agentmail/agentmail/internal/server.Version=v0.1.2".
var Version = "dev"

// SuggestedMinGatewayVersion is the minimum agentmail-gateway version this
// server release pairs with. Surfaced in /api/status and /api/info so agents
// (and ops) can compare against the gateway's self-reported version and know
// when the gateway binary is due for a swap. Bumped per release when the
// gateway half of a feature ships; overridden at build time via
// -ldflags "-X github.com/agentmail/agentmail/internal/server.SuggestedMinGatewayVersion=v0.5.5".
var SuggestedMinGatewayVersion = "dev"

// MaxSignatureLen is the maximum number of characters allowed in an account's
// directory signature. Enforced in handleProfileSelf before persisting.
const MaxSignatureLen = 200

// rateWindow is a 1-hour sliding window counter.
type rateWindow struct {
	count       int
	bytes       int64
	windowStart time.Time
}

// Server wires the store and audit log to the HTTP router.
type Server struct {
	store *store.Store
	audit *audit.Store
	cfg   *config.Config

	// Rate limiters (in-memory, 1-hour sliding window).
	rateMu       sync.Mutex
	sendRates    map[string]*rateWindow // address -> send count window
	recvRates    map[string]*rateWindow // address -> byte receive window
	declareRates map[string]*rateWindow // address -> subordinate-declare count window
	fileRates    map[string]*rateWindow // address -> attachment-management op window

	// regLimit throttles registration attempts per client IP (the threshold
	// itself lives in the store so admins can tune it live).
	regLimit *regLimiter
	// Board append windows (v1.2 point 6): per-account 10/min applies only
	// when the request carries a valid account credential; the per-code and
	// per-board ceilings always apply.
	boardAcctRate  *regLimiter
	boardCodeRate  *regLimiter
	boardBoardRate *regLimiter
	// pushSubsRate limits push subscription mutations per account;
	// pushIPLimit throttles the same per client IP (v0.6.30 abuse guard).
	pushRates   map[string]*rateWindow
	pushIPLimit *regLimiter

	// Push delivery state (v0.6.30 M2): aggregation windows, DND queues and
	// the test seam for actually sending a notification.
	pd           *pushDelivery
	sendPush     sendPushFunc // nil = real webpush sender
	vapidSubject string
}

// New builds a server with the given dependencies.
func New(s *store.Store, a *audit.Store, cfg *config.Config) *Server {
	return &Server{
		store: s, audit: a, cfg: cfg,
		sendRates:    make(map[string]*rateWindow),
		recvRates:    make(map[string]*rateWindow),
		declareRates: make(map[string]*rateWindow),
		fileRates:    make(map[string]*rateWindow),
		pushRates:    make(map[string]*rateWindow),
		pushIPLimit:  newRegLimiter(time.Hour),
		pd:           newPushDelivery(),
		regLimit:     newRegLimiter(time.Hour),

		boardAcctRate:  newRegLimiter(time.Minute),
		boardCodeRate:  newRegLimiter(time.Minute),
		boardBoardRate: newRegLimiter(time.Minute),
	}
}

// domain returns the effective mail domain: the value persisted in bbolt
// (set by the setup wizard).
func (s *Server) domain() string {
	return s.store.GetDomain()
}

// adminAddress returns the admin account address for the current domain.
// The admin local-part is fixed as "admin".
func (s *Server) adminAddress() string {
	return "admin@" + s.domain()
}

// --- rate limiting (1-hour sliding window, in-memory) ---

// checkSendRate returns an error if the account has exceeded its hourly send
// limit. On success it increments the counter.
func (s *Server) checkSendRate(address string) error {
	limit := s.store.GetSendRateLimit()
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	w := s.sendRates[address]
	now := time.Now()
	if w == nil || now.Sub(w.windowStart) >= time.Hour {
		w = &rateWindow{windowStart: now}
		s.sendRates[address] = w
	}
	if w.count >= limit {
		return fmt.Errorf("send rate limit exceeded (%d/hour)", limit)
	}
	w.count++
	return nil
}

// checkRecvRate returns false if the account has exceeded its hourly byte
// receive limit for bodyLen additional bytes. On true it updates the counter.
func (s *Server) checkRecvRate(address string, bodyLen int64) bool {
	limit := s.store.GetByteRateLimit()
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	w := s.recvRates[address]
	now := time.Now()
	if w == nil || now.Sub(w.windowStart) >= time.Hour {
		w = &rateWindow{windowStart: now}
		s.recvRates[address] = w
	}
	if w.bytes+bodyLen > limit {
		return false // would exceed
	}
	w.bytes += bodyLen
	return true
}

// Handler returns the HTTP handler for the API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/.well-known/assetlinks.json", s.handleAssetlinks)

	// Setup + status — always available (no auth, no initialization required).
	mux.HandleFunc("/setup", s.handleSetup)
	mux.HandleFunc("/api/status", s.handleStatus)

	// Public API (no auth) — requires initialization.
	mux.HandleFunc("/api/register", s.requireInitialized(s.handleRegister))
	mux.HandleFunc("/api/self", s.requireInitialized(s.handleSelfDescribe))
	mux.HandleFunc("/api/register-team", s.requireInitialized(s.handleRegisterTeam))
	mux.HandleFunc("/api/verify-password", s.requireInitialized(s.handleVerifyPassword))
	mux.HandleFunc("/api/info", s.handleInfo)

	// Authed API (account Basic auth) — requires initialization.
	mux.HandleFunc("/api/send", s.requireInitialized(s.requireAccount(s.handleSend)))
	mux.HandleFunc("/api/inbox", s.requireInitialized(s.requireAccount(s.handleInbox)))
	mux.HandleFunc("/api/search", s.requireInitialized(s.requireAccount(s.handleSearch)))
	mux.HandleFunc("/api/inbox/mark-all-read", s.requireInitialized(s.requireAccount(s.handleInboxMarkAllRead)))
	mux.HandleFunc("/api/message", s.requireInitialized(s.requireAccount(s.handleMessage)))
	mux.HandleFunc("/api/profile/self", s.requireInitialized(s.requireAccount(s.handleProfileSelf)))
	// Short alias of /api/profile/self — same handler, zero semantic drift
	// (the self-describe document advertises /api/profile; the MCP gateway
	// update_profile tool forwards to the /self path).
	mux.HandleFunc("/api/profile", s.requireInitialized(s.requireAccount(s.handleProfileSelf)))
	mux.HandleFunc("/api/account/info", s.requireInitialized(s.requireAccount(s.handleAccountInfo)))
	mux.HandleFunc("/api/contacts", s.requireInitialized(s.requireAccount(s.handleContacts)))
	mux.HandleFunc("/api/sent", s.requireInitialized(s.requireAccount(s.handleSent)))
	mux.HandleFunc("/api/mygrowth", s.requireInitialized(s.requireAccount(s.handleMyGrowth)))
	mux.HandleFunc("/api/thread", s.requireInitialized(s.requireAccount(s.handleThread)))
	mux.HandleFunc("/api/threads", s.requireInitialized(s.requireAccount(s.handleThreads)))
	mux.HandleFunc("/api/files/upload", s.requireInitialized(s.requireAccount(s.handleFileUpload)))
	mux.HandleFunc("/api/files/list", s.requireInitialized(s.requireAccount(s.handleFileList)))
	mux.HandleFunc("/api/files/", s.requireInitialized(s.requireAccount(s.handleFileDownload)))
	mux.HandleFunc("/api/password", s.requireInitialized(s.requireAccount(s.handleChangePassword)))
	// Remember-login session tokens (v0.6.27): mint with Basic auth, revoke
	// (logout) with the bearer token itself.
	mux.HandleFunc("/api/auth/token", s.requireInitialized(s.requireAccount(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			s.handleAuthTokenIssue(w, r)
		case http.MethodDelete:
			s.handleAuthTokenRevoke(w, r)
		default:
			methodNotAllowed(w)
		}
	})))
	mux.HandleFunc("/api/push/vapid-key", s.requireInitialized(s.handleVAPIDKey))
	mux.HandleFunc("/api/push/subscribe", s.requireInitialized(s.requireAccount(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			s.handlePushSubscribe(w, r)
		case http.MethodDelete:
			s.handlePushRevoke(w, r)
		default:
			methodNotAllowed(w)
		}
	})))
	mux.HandleFunc("/api/push/settings", s.requireInitialized(s.requireAccount(s.handlePushSettings)))
	mux.HandleFunc("/api/account/display-local", s.requireInitialized(s.requireAccount(s.handleDisplayLocal)))
	mux.HandleFunc("/api/site-copy", s.requireInitialized(s.handleSiteCopyGet))
	mux.HandleFunc("/admin/site-copy", s.requireInitialized(s.requireAdmin(s.handleSiteCopySet)))
	mux.HandleFunc("/api/subs", s.requireInitialized(s.requireAccount(s.handleSubs)))
	mux.HandleFunc("/api/subs/remove", s.requireInitialized(s.requireAccount(s.handleSubsRemove)))
	mux.HandleFunc("/api/subs/", s.requireInitialized(s.requireAccount(s.handleSubsMessages)))
	mux.HandleFunc("/api/mgmt/subs-overview", s.requireInitialized(s.requireAccount(s.handleMgmtSubsOverview)))
	mux.HandleFunc("/api/register-subordinate", s.requireInitialized(s.requireAccount(s.handleRegisterSubordinate)))

	// Boards (kanban) API — /api/boards/info and /api/boards/mine are
	// exact-pattern and win over the /api/boards/ subtree dispatcher.
	mux.HandleFunc("/api/worker/heartbeat", s.requireInitialized(s.requireAccount(s.handleWorkerHeartbeat)))
	mux.HandleFunc("/api/boards", s.requireInitialized(s.requireAccount(s.handleBoardCreate)))
	mux.HandleFunc("/api/boards/info", s.requireInitialized(s.handleBoardsInfo))
	mux.HandleFunc("/api/boards/mine", s.requireInitialized(s.requireAccount(s.handleBoardsMine)))
	mux.HandleFunc("/api/boards/", s.requireInitialized(s.handleBoardCode))
	mux.HandleFunc("/api/admin/boards", s.requireInitialized(s.requireAdmin(s.handleAdminBoards)))

	// Admin API (admin Basic auth) — requires initialization.
	mux.HandleFunc("/admin/messages", s.requireInitialized(s.requireAdmin(s.handleAdminMessages)))
	mux.HandleFunc("/admin/messages-all", s.requireInitialized(s.requireAdmin(s.handleAdminMessagesAll)))
	mux.HandleFunc("/admin/sent", s.requireInitialized(s.requireAdmin(s.handleAdminSent)))
	mux.HandleFunc("/admin/message", s.requireInitialized(s.requireAdmin(s.handleAdminMessage)))
	mux.HandleFunc("/admin/thread", s.requireInitialized(s.requireAdmin(s.handleAdminThread)))
	mux.HandleFunc("/admin/accounts", s.requireInitialized(s.requireAdmin(s.handleAdminAccounts)))
	mux.HandleFunc("/admin/audit", s.requireInitialized(s.requireAdmin(s.handleAdminAudit)))
	mux.HandleFunc("/admin/invalid", s.requireInitialized(s.requireAdmin(s.handleAdminInvalid)))
	mux.HandleFunc("/admin/stats", s.requireInitialized(s.requireAdmin(s.handleAdminStats)))
	mux.HandleFunc("/admin/reset-password", s.requireInitialized(s.requireAdmin(s.handleAdminResetPassword)))
	mux.HandleFunc("/admin/set-disabled", s.requireInitialized(s.requireAdmin(s.handleAdminSetDisabled)))
	mux.HandleFunc("/admin/send", s.requireInitialized(s.requireAdmin(s.handleAdminSend)))
	mux.HandleFunc("/admin/settings", s.requireInitialized(s.requireAdmin(s.handleAdminSettings)))
	mux.HandleFunc("/admin/set-registration", s.requireInitialized(s.requireAdmin(s.handleAdminSetRegistration)))
	mux.HandleFunc("/admin/set-oneclick-register", s.requireInitialized(s.requireAdmin(s.handleAdminSetOneclickRegister)))
	mux.HandleFunc("/admin/set-random-register", s.requireInitialized(s.requireAdmin(s.handleAdminSetRandomRegister)))
	mux.HandleFunc("/admin/normalize-account-case", s.requireInitialized(s.requireAdmin(s.handleAdminNormalizeAccountCase)))
	mux.HandleFunc("/admin/set-showcase", s.requireInitialized(s.requireAdmin(s.handleAdminSetShowcase)))
	mux.HandleFunc("/admin/set-danmaku", s.requireInitialized(s.requireAdmin(s.handleAdminSetDanmaku)))
	mux.HandleFunc("/admin/clear-showcase", s.requireInitialized(s.requireAdmin(s.handleAdminClearShowcase)))
	mux.HandleFunc("/admin/delete-showcase-item", s.requireInitialized(s.requireAdmin(s.handleAdminDeleteShowcaseItem)))
	mux.HandleFunc("/admin/showcase-item", s.requireInitialized(s.requireAdmin(s.handleAdminGetShowcaseItem)))
	mux.HandleFunc("/admin/set-directory-listed", s.requireInitialized(s.requireAdmin(s.handleAdminSetDirectoryListed)))
	mux.HandleFunc("/admin/set-limits", s.requireInitialized(s.requireAdmin(s.handleAdminSetLimits)))

	// Admin web panel: static files under /static/*, plus the index page at "/".
	// These are always served (the panel JS checks /api/status to decide
	// whether to show the setup wizard or the normal UI). Both carry a strong
	// ETag + Cache-Control: no-cache so a new release is never masked by a
	// cached shell (v0.1.7: stale-shell false reports, superior order).
	mux.HandleFunc("/static/", handleStaticCached)
	mux.HandleFunc("/", s.serveIndex)

	// Unmatched /api/* paths: plain-text 404 (weber ③: no Go default page
	// on the API face). Explicit routes above always win (ServeMux
	// longest-pattern).
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})

	// OPTIONS is rejected outright (weber ②): the mux is method-blind and
	// an OPTIONS used to execute the GET handler and return data. Real
	// methods keep their existing behavior; same-origin panel has no
	// preflight needs.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.Header().Set("Allow", "GET, HEAD, POST, DELETE")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// requireInitialized gates a handler on the system being bootstrapped. Before
// initialization, every data endpoint returns 503 so the only paths that work
// are /healthz, /setup, /api/status, and the static panel (which shows the
// setup wizard).
func (s *Server) requireInitialized(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.store.IsInitialized() {
			http.Error(w, "not initialized", http.StatusServiceUnavailable)
			return
		}
		h(w, r)
	}
}

// serveIndex returns the embedded index.html for the panel root. It is the
// unauthenticated entry point: the browser will prompt for Basic auth when the
// page's first admin fetch runs.
func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	// Only serve the index at exactly "/"; anything else is a 404.
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(staticSubFS, "index.html")
	if err != nil {
		http.Error(w, "index not found", http.StatusInternalServerError)
		return
	}
	serveIndexCached(w, r, data)
}

// ListenAndServe starts the HTTP server. Blocks until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.Server.Listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	// File TTL: sweep expired uploads at startup, then daily.
	go s.filesTTLLoop(ctx)
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	return srv.ListenAndServe()
}

// filesTTLLoop evicts expired attachment files at startup and every 24h.
func (s *Server) filesTTLLoop(ctx context.Context) {
	sweep := func() {
		if n, err := s.store.CleanupExpiredFiles(); err != nil {
			fmt.Printf("files ttl sweep error: %v\n", err)
		} else if n > 0 {
			fmt.Printf("files ttl sweep: evicted %d expired files\n", n)
		}
	}
	sweep()
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweep()
		}
	}
}

// requireAccount wraps a handler so that it only runs after a valid non-admin
// account credential is presented via HTTP Basic auth. The authenticated
// address is passed to the handler via the request context.
func (s *Server) requireAccount(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		address, ok := s.authAccount(w, r)
		if !ok {
			return // already wrote 401
		}
		h.ServeHTTP(w, r.WithContext(withAccount(r.Context(), address)))
	}
}

// authAccount accepts EITHER a bearer session token (remember-login,
// v0.6.27) or the classic Basic credential as a fallback. Both paths write
// 401 on failure; a present-but-invalid bearer does NOT fall through to
// Basic parsing (a token client sending Basic-less garbage should learn
// its token died, not masquerade as a password client).
func (s *Server) authAccount(w http.ResponseWriter, r *http.Request) (string, bool) {
	if tok := bearerToken(r.Header.Get("Authorization")); tok != "" {
		addr, err := s.store.ResolveSessionToken(tok)
		if err != nil {
			unauthorized(w)
			return "", false
		}
		return addr, true
	}
	return s.basicAuthAccount(w, r)
}

// bearerToken extracts the token from "Authorization: Bearer <token>"
// (case-insensitive scheme), or "".
func bearerToken(header string) string {
	const prefix = "bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return header[len(prefix):]
}

// requireAdmin wraps a handler so that it only runs for the configured admin
// credential.
func (s *Server) requireAdmin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.basicAuthAdmin(w, r) {
			return
		}
		h.ServeHTTP(w, r)
	}
}

// basicAuthAccount validates a Basic auth header against a real account and
// returns the address on success. Writes 401 and returns false on failure.
func (s *Server) basicAuthAccount(w http.ResponseWriter, r *http.Request) (string, bool) {
	user, pass, ok := parseBasicAuth(r.Header.Get("Authorization"))
	if !ok {
		unauthorized(w)
		return "", false
	}
	// The admin credential also satisfies account auth (the admin is an
	// account), but admin-only endpoints use requireAdmin separately.
	if err := s.store.VerifyPassword(user, pass); err != nil {
		unauthorized(w)
		return "", false
	}
	return user, true
}

// basicAuthAdmin validates a Basic auth header against a stored admin account.
// Admin credentials are looked up in bbolt (not the config file), so an admin
// who resets their password via the panel keeps working after the change and
// across restarts. The config file's [admin] section only seeds the initial
// admin account at first startup (see EnsureAdmin).
//
// A credential pair is admin-valid iff:
//   - the account exists in bbolt,
//   - the bcrypt hash matches, and
//   - the account has IsAdmin == true.
func (s *Server) basicAuthAdmin(w http.ResponseWriter, r *http.Request) bool {
	// Bearer session tokens (remember-login) must clear the admin gate too:
	// the panel's post-login /admin/* calls carry the token, not Basic. A
	// logged-in admin getting 401 here bounced the whole panel to login
	// (v0.1.3 P1: the session-role fix lit this path up for real admins).
	// Semantics mirror authAccount: a present-but-invalid bearer is final.
	if tok := bearerToken(r.Header.Get("Authorization")); tok != "" {
		addr, err := s.store.ResolveSessionToken(tok)
		if err != nil {
			unauthorized(w)
			return false
		}
		acc, err := s.store.GetAccount(addr)
		if err != nil || !acc.IsAdmin {
			unauthorized(w)
			return false
		}
		return true
	}
	user, pass, ok := parseBasicAuth(r.Header.Get("Authorization"))
	if !ok {
		unauthorized(w)
		return false
	}
	acc, err := s.store.GetAccount(user)
	if err != nil {
		unauthorized(w)
		return false
	}
	if err := bcryptCompare(acc.PasswordHash, []byte(pass)); err != nil {
		unauthorized(w)
		return false
	}
	if !acc.IsAdmin {
		unauthorized(w)
		return false
	}
	return true
}

// --- helpers ---

type ctxKey int

const accountKey ctxKey = 1

func withAccount(ctx context.Context, address string) context.Context {
	return context.WithValue(ctx, accountKey, address)
}

func accountFrom(ctx context.Context) string {
	v, _ := ctx.Value(accountKey).(string)
	return v
}

// parseBasicAuth splits an "Authorization: Basic ..." header.
func parseBasicAuth(header string) (user, pass string, ok bool) {
	const prefix = "Basic "
	if !strings.HasPrefix(header, prefix) {
		return "", "", false
	}
	dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(header[len(prefix):]))
	if err != nil {
		return "", "", false
	}
	parts := strings.SplitN(string(dec), ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// unauthorized writes a 401 WITHOUT a WWW-Authenticate header. The panel now
// manages credentials in JS (sessionStorage + a Basic auth header on each
// request); sending WWW-Authenticate would make the browser pop its native
// login dialog on a 401, fighting the JS login flow. The JS api() wrapper
// handles 401 itself (clears the session and shows the login page).
func unauthorized(w http.ResponseWriter) {
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// bcryptCompare is a thin wrapper kept here so the server package does not
// reach into store internals for the admin auth check.
func bcryptCompare(hash, password []byte) error {
	return bcrypt.CompareHashAndPassword(hash, password)
}
