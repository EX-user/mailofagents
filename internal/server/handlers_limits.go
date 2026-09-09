package server

// Per-account recipient/cc count limits (boss 2026-09-09: adjustable caps
// on "to" and "cc" sizes — mass sends / large cc are the mailbox-bloat
// source, so the cap itself is the治理 lever, no extra machinery).
//
// Who may view/adjust a target account's limits:
//   - the account itself,
//   - any of its direct superiors (bSubs edge, superior→subordinate),
//   - the admin.
//
// Limits are ints in [0, store.MaxRecipientLimitCap]; 0 = unlimited (the
// default on old records — no migration). POST fields are pointers:
// omitted = keep current, explicit 0 = lift the cap.

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/agentmail/agentmail/internal/audit"
	"github.com/agentmail/agentmail/internal/store"
)

// canManageLimits reports whether the authenticated caller (who) may view
// or adjust the target account's recipient limits.
func (s *Server) canManageLimits(r *http.Request, who, target string) bool {
	if strings.EqualFold(who, target) {
		return true
	}
	if s.store.IsSubordinate(who, target) {
		return true // who is a direct superior of target
	}
	return s.callerIsAdmin(r)
}

func (s *Server) handleAccountLimits(w http.ResponseWriter, r *http.Request) {
	who := accountFrom(r.Context())
	switch r.Method {
	case http.MethodGet:
		target := r.URL.Query().Get("address")
		if target == "" {
			target = who
		}
		if !s.canManageLimits(r, who, target) {
			http.Error(w, "not allowed to view this account's limits", http.StatusForbidden)
			return
		}
		acc, err := s.store.GetAccount(target)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"address":        acc.Address,
			"max_recipients": acc.MaxRecipients,
			"max_cc":         acc.MaxCC,
		})
	case http.MethodPost:
		var body struct {
			Address       *string `json:"address"`        // nil = self
			MaxRecipients *int    `json:"max_recipients"` // nil = keep; 0 = unlimited
			MaxCC         *int    `json:"max_cc"`         // nil = keep; 0 = unlimited
		}
		if err := decodeJSON(r, &body); err != nil {
			badRequest(w, "invalid body: "+err.Error())
			return
		}
		target := who
		if body.Address != nil && strings.TrimSpace(*body.Address) != "" {
			target = strings.TrimSpace(*body.Address)
		}
		if !s.canManageLimits(r, who, target) {
			http.Error(w, "not allowed to manage this account's limits", http.StatusForbidden)
			return
		}
		cur, err := s.store.GetAccount(target)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		maxTo, maxCC := cur.MaxRecipients, cur.MaxCC
		for field, val := range map[string]*int{
			"max_recipients": body.MaxRecipients,
			"max_cc":         body.MaxCC,
		} {
			if val == nil {
				continue
			}
			if *val < 0 || *val > store.MaxRecipientLimitCap {
				badRequest(w, fmt.Sprintf("%s must be between 0 and %d (0 = no limit)", field, store.MaxRecipientLimitCap))
				return
			}
		}
		if body.MaxRecipients != nil {
			maxTo = *body.MaxRecipients
		}
		if body.MaxCC != nil {
			maxCC = *body.MaxCC
		}
		if err := s.store.SetRecipientLimits(target, maxTo, maxCC); err != nil {
			internalError(w, "set recipient limits: "+err.Error())
			return
		}
		_ = s.audit.Record(r.Context(), audit.ActionRecipientLimits, who,
			fmt.Sprintf("target=%s max_recipients=%d max_cc=%d", target, maxTo, maxCC))
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":             true,
			"address":        target,
			"max_recipients": maxTo,
			"max_cc":         maxCC,
		})
	default:
		methodNotAllowed(w)
	}
}
