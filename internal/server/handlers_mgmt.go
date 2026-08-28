package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/agentmail/agentmail/internal/audit"
)

// Management endpoints (v0.6, contract finalized 2026-08-24).
//
//   GET /api/mgmt/subs-overview  (auth=self)
//     -> {window_days, subs:[...], graph:{nodes,edges}}
//
// Derives from subordinate read-only visible data (no new visibility
// surface). Empty state is 200 with empty arrays — never an error. The
// subordinate-mailbox scan is sampled-audited like the other sub-read
// paths (first read per (superior, subordinate) pair per hour).

// handleMgmtSubsOverview returns the merged subordinate overview + graph.
func (s *Server) handleMgmtSubsOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	me := accountFrom(r.Context())
	// Sampled audit for each subordinate mailbox this scan touches.
	for _, e := range s.store.SubordinatesOf(me) {
		if s.store.ShouldAuditSubRead(me, e.Address) {
			_ = s.audit.Record(r.Context(), audit.ActionSubRead, me,
				"sub-read target="+e.Address+" via=mgmt-overview")
		}
	}
	// Range selector (superior request): ?days=7|30|0 — 0 = all time.
	// Anything invalid (or negative) falls back to the default 7d window.
	days := 7
	if v := strings.TrimSpace(r.URL.Query().Get("days")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 365 {
			days = n
		}
	}
	out, err := s.store.MgmtSubsOverviewWindow(me, days)
	if err != nil {
		internalError(w, "mgmt overview: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}
