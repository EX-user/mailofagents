package server

import (
	"net/http"
	"strings"

	"github.com/agentmail/agentmail/internal/audit"
)

// handleSearch serves the content-search endpoint shared by the inbox card
// and the mail-management browse page (boss-directed single call point).
//
//   GET /api/search?q=<query>&limit=&offset=&box=in|out|both&account=<addr>
//
// - q is required; matching is case-insensitive substring over
//   subject+from+to+cc+body, newest-first, limit/offset paging identical to
//   /api/inbox (total_count is match-count across the whole scanned range).
// - account defaults to the caller. A non-self account must be the caller's
//   subordinate: no relationship and no account look identical (404, same
//   masquerade rule as GET /api/subs/{A}). Subordinate targets always scan
//   both boxes (box ignored, matching the browse page's all-folders view) and
//   the read goes through the same sub-read audit as /api/subs/{A}/message.
// - box (in|out|both, default both) applies only to self searches.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	who := accountFrom(r.Context())
	q := r.URL.Query().Get("q")
	if strings.TrimSpace(q) == "" {
		badRequest(w, "q query parameter is required")
		return
	}
	limit := queryInt(r, "limit", 20)
	offset := queryInt(r, "offset", 0)
	accountParam := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("account")))

	var boxes []string
	boxEcho := "both"
	respAccount := who
	if accountParam == "" || accountParam == strings.ToLower(who) {
		box := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("box")))
		switch box {
		case "", "both":
			boxes = []string{"in", "out"}
		case "in", "out":
			boxes = []string{box}
			boxEcho = box
		default:
			badRequest(w, "box must be in|out|both")
			return
		}
	} else {
		if !s.store.IsSubordinate(who, accountParam) {
			http.NotFound(w, r)
			return
		}
		if s.store.ShouldAuditSubRead(who, accountParam) {
			_ = s.audit.Record(r.Context(), audit.ActionSubRead, who, "sub-search target="+accountParam)
		}
		boxes = []string{"in", "out"}
		respAccount = accountParam
	}
	msgs, total, err := s.store.SearchAccount(respAccount, boxes, q, limit, offset)
	if err != nil {
		internalError(w, "search: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"messages":    msgs,
		"total_count": total,
		"account":     respAccount,
		"box":         boxEcho,
	})
}
