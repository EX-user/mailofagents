package server

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/agentmail/agentmail/internal/store"
)

// handleInfo is a general-purpose query endpoint that returns structured
// information about the server. The "query" parameter selects what to return.
// This is designed so that new query types can be added on the SERVER side
// without requiring gateway changes — the gateway's server_info tool is a
// thin pass-through that forwards the query string here.
//
//	GET /api/info?query=status     -> version, domain, initialized (public)
//	GET /api/info?query=stats      -> account/message counts (public)
//	GET /api/info?query=settings   -> registration + rate limits (public)
//	GET /api/info?query=directory  -> public address book of opt-in accounts (public)
//	GET /api/info?query=growth     -> message counts by age bucket (public)
//	GET /api/info?query=accounts   -> account list (admin only)
//	GET /api/info?query=audit      -> recent audit log (admin only)
//	GET /api/info?query=help       -> list of available queries (public)
//
// Auth rules:
//   - "accounts" requires admin Basic auth.
//   - All others are public (no auth needed).
func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	query := r.URL.Query().Get("query")
	if query == "" {
		query = "help"
	}

	switch query {
	case "status":
		s.infoStatus(w, r)

	case "stats":
		s.infoStats(w, r)

	case "settings":
		s.infoSettings(w, r)

	case "accounts":
		// Admin-only: re-authenticate as admin.
		s.infoAccounts(w, r)

	case "audit":
		// Admin-only.
		s.infoAudit(w, r)

	case "directory":
		// Public: list accounts that opted into the directory.
		s.infoDirectory(w, r)

	case "growth":
		// Public: message-volume growth stats for the guest portal.
		s.infoGrowth(w, r)

	case "showcase":
		// Public: random sample of opt-in public messages (portal showcase).
		s.infoShowcase(w, r)

	case "help":
		s.infoHelp(w, r)

	default:
		badRequest(w, "unknown query: "+query+". Use query=help to see options.")
	}
}

func (s *Server) infoStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"query":                         "status",
		"version":                       Version,
		"domain":                        s.domain(),
		"initialized":                   s.store.IsInitialized(),
		"suggested_min_gateway_version": SuggestedMinGatewayVersion,
	})
}

func (s *Server) infoStats(w http.ResponseWriter, r *http.Request) {
	accountCount, _ := s.store.CountAccounts()
	messageCount, _ := s.store.CountMessages()
	writeJSON(w, http.StatusOK, map[string]any{
		"query":         "stats",
		"account_count": accountCount,
		"message_count": messageCount,
		"db_size_bytes": s.store.DBSizeBytes(),
	})
}

func (s *Server) infoSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"query":                     "settings",
		"registration_enabled":      s.store.IsRegistrationEnabled(),
		"directory_listed_enabled":  s.store.IsDirectoryListedEnabled(),
		"oneclick_register_enabled": s.store.IsOneclickRegisterEnabled(),
		"random_register_enabled":   s.store.IsRandomRegisterEnabled(),
		"showcase_enabled":          s.store.IsShowcaseEnabled(),
		"danmaku_default_mode":      s.store.GetDanmakuDefaultMode(),
		"danmaku_default_speed":     s.store.GetDanmakuDefaultSpeed(),
		"danmaku_default_count":     s.store.GetDanmakuDefaultCount(),
		"send_rate_limit":           s.store.GetSendRateLimit(),
		"byte_rate_limit":           s.store.GetByteRateLimit(),
		"register_rate_limit":       s.store.GetRegisterIPRateLimit(),
		"files_total_limit":         s.store.GetFilesTotalLimit(),
		"file_quota_per_acct":       s.store.GetFileQuotaPerAcct(),
	})
}

// infoAccounts requires admin auth. It's mounted under /api/info but does its
// own admin check — if the caller is not admin, it returns 403.
func (s *Server) infoAccounts(w http.ResponseWriter, r *http.Request) {
	// Check admin credentials via Basic Auth.
	address, password, ok := r.BasicAuth()
	if !ok {
		// No WWW-Authenticate header: the JS panel manages auth itself, and
		// that header would trigger the browser's native login popup.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	acct, err := s.store.GetAccount(address)
	if err != nil || bcryptCompare([]byte(acct.PasswordHash), []byte(password)) != nil || !acct.IsAdmin {
		http.Error(w, "forbidden: admin only", http.StatusForbidden)
		return
	}

	accounts, err := s.store.ListAccounts()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	type accountInfo struct {
		Address   string `json:"address"`
		IsAdmin   bool   `json:"is_admin"`
		Disabled  bool   `json:"disabled"`
		Visible   bool   `json:"visible"`
		Signature string `json:"signature"`
		CreatedAt int64  `json:"created_at"`
	}
	// Return-layer merge: legacy mixed-case twin rows collapse into their
	// lowercase twin (same as /admin/accounts — data untouched).
	rows := dedupeAccountsByLowerKey(accounts,
		func(a store.Account) string { return a.Address },
		func(a *store.Account) { a.Address = strings.ToLower(a.Address) })
	var list []accountInfo
	for _, a := range rows {
		list = append(list, accountInfo{
			Address:   a.Address,
			IsAdmin:   a.IsAdmin,
			Disabled:  a.Disabled,
			Visible:   a.Visible,
			Signature: a.Signature,
			CreatedAt: a.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"query":    "accounts",
		"count":    len(list),
		"accounts": list,
	})
}

// infoAudit requires admin auth.
func (s *Server) infoAudit(w http.ResponseWriter, r *http.Request) {
	address, password, ok := r.BasicAuth()
	if !ok {
		// No WWW-Authenticate header: the JS panel manages auth itself, and
		// that header would trigger the browser's native login popup.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	acct, err := s.store.GetAccount(address)
	if err != nil || bcryptCompare([]byte(acct.PasswordHash), []byte(password)) != nil || !acct.IsAdmin {
		http.Error(w, "forbidden: admin only", http.StatusForbidden)
		return
	}

	entries, _ := s.audit.List(r.Context(), 50)
	writeJSON(w, http.StatusOK, map[string]any{
		"query":   "audit",
		"count":   len(entries),
		"entries": entries,
	})
}

// infoDirectory returns the public directory: every account that has opted in
// via Visible=true (disabled accounts are excluded). No auth required — this is
// the public address book. Only address + signature are exposed per entry.
func (s *Server) infoDirectory(w http.ResponseWriter, r *http.Request) {
	visible, err := s.store.ListVisibleAccounts()
	if err != nil {
		internalError(w, "list visible: "+err.Error())
		return
	}
	type dirEntry struct {
		Address   string `json:"address"`
		Signature string `json:"signature"`
	}
	rows := dedupeAccountsByLowerKey(visible,
		func(a store.Account) string { return a.Address },
		func(a *store.Account) { a.Address = strings.ToLower(a.Address) })
	entries := make([]dirEntry, 0, len(rows))
	for _, a := range rows {
		entries = append(entries, dirEntry{Address: a.Address, Signature: a.Signature})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"query":   "directory",
		"count":   len(entries),
		"entries": entries,
	})
}

// growthCache memoizes the last computed Growth result. The endpoint is
// public (it feeds the guest portal), so we scan at most once per TTL
// instead of on every hit; a scan error keeps serving the last good value.
var (
	growthMu     sync.Mutex
	growthCached store.Growth
	growthAt     time.Time
)

const growthCacheTTL = 60 * time.Second

// infoGrowth returns message counts bucketed by age (today / week / month /
// total). Public: activity stats for the guest portal homepage.
func (s *Server) infoGrowth(w http.ResponseWriter, r *http.Request) {
	growthMu.Lock()
	if growthAt.IsZero() || time.Since(growthAt) > growthCacheTTL {
		if g, err := s.store.MessageGrowth(time.Now()); err == nil {
			growthCached = g
			growthAt = time.Now()
		}
		// On error: fall through and serve the stale value (a slightly old
		// count beats a broken portal).
	}
	g := growthCached
	growthMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"query": "growth",
		"today": g.Today,
		"week":  g.Week,
		"month": g.Month,
		"total": g.Total,
		"days":  g.Days,
	})
}

// showcaseBodyLimit is how much of a public message body the showcase
// endpoint exposes — a sample strip, not the full text.
const showcaseBodyLimit = 200

// infoShowcase returns the latest public (sender-opted-in) messages for the
// portal showcase, newest first. GET /api/info?query=showcase[&n=10]; n is
// clamped to 1..50. Bodies are truncated to showcaseBodyLimit runes. Only
// from/subject/body/ts are exposed — never the recipients. (Was a random
// sample; admin asked for time-ordered latest N.)
func (s *Server) infoShowcase(w http.ResponseWriter, r *http.Request) {
	// NOTE: showcase_enabled deliberately does NOT gate this endpoint. Per
	// admin's clarification the toggle only controls the Compose "public"
	// checkbox visibility; the portal showcase keeps serving public mail
	// regardless (it hides itself client-side when there is no data).
	n := queryInt(r, "n", 10)
	if n < 1 {
		n = 1
	}
	if n > 50 {
		n = 50
	}
	entries, err := s.store.ListShowcase()
	if err != nil {
		internalError(w, "list showcase: "+err.Error())
		return
	}
	// ListShowcase is already newest-first; take the latest n.
	if len(entries) > n {
		entries = entries[:n]
	}
	type showcaseItem struct {
		ID      string `json:"id"`
		From    string `json:"from"`
		Subject string `json:"subject"`
		Body    string `json:"body"`
		Ts      int64  `json:"ts"`
	}
	items := make([]showcaseItem, 0, len(entries))
	for _, e := range entries {
		body := e.Body
		if runes := []rune(body); len(runes) > showcaseBodyLimit {
			body = string(runes[:showcaseBodyLimit]) + "…"
		}
		items = append(items, showcaseItem{ID: e.ID, From: e.From, Subject: e.Subject, Body: body, Ts: e.ReceivedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"query": "showcase",
		"count": len(items),
		"items": items,
	})
}

func (s *Server) infoHelp(w http.ResponseWriter, r *http.Request) {
	queries := []map[string]any{
		{"query": "status", "auth": "none", "description": "Server version, domain, initialization state"},
		{"query": "stats", "auth": "none", "description": "Account and message counts"},
		{"query": "settings", "auth": "none", "description": "Registration toggle and rate limit values"},
		{"query": "directory", "auth": "none", "description": "Public address book: accounts that opted in (Visible=true) with their signature"},
		{"query": "growth", "auth": "none", "description": "Message counts by age (today / 7d / 30d / total) plus a 7-day per-day array for charts"},
		{"query": "showcase", "auth": "none", "description": "Latest sender-opted-in public messages, newest first (n param, default 10, max 50; bodies truncated)"},
		{"query": "accounts", "auth": "admin", "description": "Full account list with admin/disabled/created flags"},
		{"query": "audit", "auth": "admin", "description": "Recent 50 audit log entries"},
		{"query": "help", "auth": "none", "description": "This list"},
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"query":   "help",
		"queries": queries,
	})
}
