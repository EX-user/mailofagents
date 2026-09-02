package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/agentmail/agentmail/internal/audit"
	"github.com/agentmail/agentmail/internal/store"
)

// Subordinate-account API (v1). A (the authenticated account) declares
// itself a subordinate of B; B can then read A's inbox+sent (scope is a
// reserved field, v1 always "both"). Self-declared, revocable, queries
// never recurse. 404 masquerade: unauthorized relationship reads look
// identical to "no such account" so existence of a relationship is not
// leaked.
//
//   POST   /api/subs              {"superior": "b@x", "scope": "both"}   (auth = A)
//   DELETE /api/subs?superior=...                                          (auth = A)
//   GET    /api/subs                                                        (auth = caller: own edges both ways)
//   GET    /api/subs/{A}/messages?folder=inbox|sent|both&limit=            (auth = B)
//   POST   /api/subs/remove         {"address": "x@y"}                     (auth = either end; v0.6.5)

// declareRateLimit is the per-account hourly cap on declare calls (anti
// spamming of relationship edges; revokes are free).
const declareRateLimit = 10

// handleSubs dispatches /api/subs by method: GET = list own edges,
// POST = declare, DELETE = revoke.
func (s *Server) handleSubs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleSubsList(w, r)
	case http.MethodPost:
		s.handleSubsDeclare(w, r)
	case http.MethodDelete:
		s.handleSubsRevoke(w, r)
	default:
		methodNotAllowed(w)
	}
}

// handleSubsList returns the caller's own edges: subordinates (who declared
// under me) and superiors (who I declared under).
func (s *Server) handleSubsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	me := accountFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"subordinates": s.store.SubordinatesOf(me),
		"superiors":    s.store.SuperiorsOf(me),
	})
}

// handleSubsDeclare declares the authenticated account (A) a subordinate of
// body.superior (B). Idempotent; audited.
func (s *Server) handleSubsDeclare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	me := accountFrom(r.Context())
	var body struct {
		Superior string `json:"superior"`
		Scope    string `json:"scope"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, "invalid body: "+err.Error())
		return
	}
	if strings.TrimSpace(body.Superior) == "" {
		badRequest(w, "superior is required")
		return
	}
	// Scope field is reserved: v1 only accepts "both" (or omitted).
	if body.Scope != "" && body.Scope != "both" {
		badRequest(w, "scope values other than \"both\" are not supported in v1")
		return
	}
	// Anti-spam: cap declares per account per hour.
	s.rateMu.Lock()
	now := time.Now()
	rw := s.declareRates[me]
	if rw == nil || now.Sub(rw.windowStart) >= time.Hour {
		rw = &rateWindow{windowStart: now}
		s.declareRates[me] = rw
	}
	if rw.count >= declareRateLimit {
		s.rateMu.Unlock()
		http.Error(w, fmt.Sprintf("declare rate limit exceeded (%d/hour)", declareRateLimit), http.StatusTooManyRequests)
		return
	}
	rw.count++
	s.rateMu.Unlock()

	if err := s.store.DeclareSubordinate(body.Superior, me); err != nil {
		if err == store.ErrNoSuchAccount {
			http.Error(w, "no such account", http.StatusNotFound)
			return
		}
		s.badRequestErr(w, r, err)
		return
	}
	_ = s.audit.Record(r.Context(), audit.ActionSubDeclare, me, "declare-sub superior="+body.Superior)
	writeJSON(w, http.StatusCreated, map[string]any{
		"status":   "declared",
		"superior": body.Superior,
		"scope":    "both",
	})
}

// handleSubsRevoke removes the authenticated account's (A's) declaration
// under the given superior (B). Idempotent; audited. Takes effect on the
// very next read (no caching).
func (s *Server) handleSubsRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		methodNotAllowed(w)
		return
	}
	me := accountFrom(r.Context())
	superior := strings.TrimSpace(r.URL.Query().Get("superior"))
	if superior == "" {
		badRequest(w, "superior query parameter is required")
		return
	}
	if err := s.store.RevokeSubordinate(superior, me); err != nil {
		if err == store.ErrNoSuchAccount {
			http.Error(w, "no such account", http.StatusNotFound)
			return
		}
		s.badRequestErr(w, r, err)
		return
	}
	_ = s.audit.Record(r.Context(), audit.ActionSubRevoke, me, "revoke-sub superior="+superior)
	writeJSON(w, http.StatusOK, map[string]any{"status": "revoked", "superior": superior})
}

// handleSubsRemove implements the v0.6.5 bidirectional removal contract
// (alice 01M0VCEDB): the authenticated account removes its subordinate
// relationship with {address} — the unique edge is deleted regardless of
// which end the initiator occupies, and the server auto-sends a system
// notification mail to the other party in the same transaction. A missing
// relationship is 404, matching the subs masquerade semantics (no
// existence leak); no idempotency beyond that.
func (s *Server) handleSubsRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	me := accountFrom(r.Context())
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	var body struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		badRequest(w, "invalid JSON body")
		return
	}
	addr := strings.ToLower(strings.TrimSpace(body.Address))
	if addr == "" {
		badRequest(w, "address is required")
		return
	}
	role, err := s.store.RemoveSubordinate(strings.ToLower(me), addr)
	if errors.Is(err, store.ErrNoSubRelationship) {
		http.Error(w, "no such relationship", http.StatusNotFound)
		return
	}
	if err != nil {
		internalError(w, err.Error())
		return
	}
	_ = s.audit.Record(r.Context(), audit.ActionSubRemoved, me, "sub-remove target="+addr+" initiator_role="+role)
	writeJSON(w, http.StatusOK, map[string]any{
		"removed":        true,
		"initiator_role": role,
	})
}

// handleSubsMessages lets B (authenticated) read A's messages. Authorization
// is checked first and failures masquerade as 404 so the existence of a
// relationship is never leaked. Attachment access codes are stripped (Q2
// ruling: metadata only — download requires A's explicit grant).
func (s *Server) handleSubsMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	me := accountFrom(r.Context())
	// Path: /api/subs/{A}/messages (list) or /api/subs/{A}/message (detail).
	segs := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(segs) != 4 || (segs[3] != "messages" && segs[3] != "message") {
		http.NotFound(w, r)
		return
	}
	target := segs[2]

	// Self-read: the flat "all visible accounts" view may address the
	// caller's OWN mailbox through this uniform path. That must behave
	// exactly like /api/message — including clearing the unread marker
	// (subordinate reads deliberately never touch A's read state, which
	// made the own-inbox badge stuck when the panel routed self-reads
	// here) — and must not require a self-relationship (which 404'd).
	if strings.EqualFold(me, target) {
		if segs[3] == "message" {
			id := strings.TrimSpace(r.URL.Query().Get("id"))
			if id == "" {
				badRequest(w, "id query parameter is required")
				return
			}
			msg, err := s.store.GetMessage(me, id) // normal semantics: visibility + MarkRead
			if err != nil {
				http.NotFound(w, r)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"subordinate": target,
				"message":     *msg, // own mailbox: attachment codes visible, nothing stripped
			})
			return
		}
		folder := r.URL.Query().Get("folder")
		if folder == "" {
			folder = "both"
		}
		limit := 50
		if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l <= 200 {
			limit = l
		}
		msgs, err := s.store.ReadSubordinateMessages(me, folder, limit)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"subordinate": target,
			"folder":      folder,
			"count":       len(msgs),
			"messages":    msgs,
		})
		return
	}

	// The masquerade: no relationship and no account look identical.
	if !s.store.IsSubordinate(me, target) {
		http.NotFound(w, r)
		return
	}

	// Detail: GET /api/subs/{A}/message?id=<messageID> — full body, cc
	// included, attachment access codes stripped (Q2: metadata only).
	if segs[3] == "message" {
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			badRequest(w, "id query parameter is required")
			return
		}
		if s.store.ShouldAuditSubRead(me, target) {
			_ = s.audit.Record(r.Context(), audit.ActionSubRead, me, "sub-read target="+target)
		}
		msg, err := s.store.GetSubordinateMessage(id)
		if err != nil {
			http.NotFound(w, r) // same masquerade shape
			return
		}
		// The message must belong to the subordinate's view (their inbox or
		// sent reference) — otherwise B could fish arbitrary message ids.
		if !s.store.MessageReferencedBy(target, id) {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"subordinate": target,
			"message":     stripAttachmentCodes(*msg),
		})
		return
	}

	// Sampled audit: first read per (B,A) per hour is recorded.
	if s.store.ShouldAuditSubRead(me, target) {
		_ = s.audit.Record(r.Context(), audit.ActionSubRead, me, "sub-read target="+target)
	}

	folder := r.URL.Query().Get("folder")
	if folder == "" {
		folder = "both"
	}
	limit := 50
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l <= 200 {
		limit = l
	}
	msgs, err := s.store.ReadSubordinateMessages(target, folder, limit)
	if err != nil {
		http.NotFound(w, r) // target vanished mid-request — same masquerade
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"subordinate": target,
		"folder":      folder,
		"count":       len(msgs),
		"messages":    msgs,
	})
}

// stripAttachmentCodes returns a copy of the message with every
// attachment's access code removed — metadata only for the superior's view
// (Q2 ruling: download requires the subordinate's explicit grant).
func stripAttachmentCodes(m store.Message) store.Message {
	if len(m.Attachments) == 0 {
		return m
	}
	m.Attachments = append([]store.AttachmentMeta(nil), m.Attachments...)
	for i := range m.Attachments {
		m.Attachments[i].AccessCode = ""
	}
	return m
}

// handleRegisterSubordinate creates a fresh random account and declares it
// a subordinate of the authenticated caller, in one step — the panel's
// "register subordinate account" button (mrf2000/上级 request via alice's
// new address). Naming/password reuse the one-click register generators;
// the one-time password is returned exactly once (same semantics as
// /api/register). Deliberately NOT exposed through MCP: agents compose
// register + subs API themselves.
//
//	POST /api/register-subordinate (account auth, no body)
//	-> {"address": "...", "password": "...", "declared": true}
func (s *Server) handleRegisterSubordinate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	owner := accountFrom(r.Context())

	// Guard 1: cap the number of subordinates one account may provision.
	if got := len(s.store.SubordinatesOf(owner)); got >= store.MaxSubordinates {
		http.Error(w, fmt.Sprintf("subordinate limit reached (%d)", store.MaxSubordinates), http.StatusTooManyRequests)
		return
	}
	// Guard 2: reuse the per-IP registration throttle.
	if !s.regLimit.allow(clientIP(r), s.store.GetRegisterIPRateLimit(), time.Now()) {
		http.Error(w, "too many registrations from this address, try again later", http.StatusTooManyRequests)
		return
	}

	// v0.6.4 contract: an optional {username} names the subordinate
	// explicitly. Taken names answer 409 (the one-time password display
	// must match what the caller typed — no silent renames). Absent/empty
	// body keeps the random bot-<8> behavior.
	domain := s.domain()
	named := ""
	if raw, _ := io.ReadAll(io.LimitReader(r.Body, 4096)); len(bytes.TrimSpace(raw)) > 0 {
		var body struct {
			Username string `json:"username"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			badRequest(w, "invalid body: "+err.Error())
			return
		}
		named = strings.TrimSpace(body.Username)
		if named != "" {
			if len(named) > 32 || !isASCIILocalPart(named) {
				badRequest(w, "username must be ASCII letters, digits, '-' or '_' (max 32 chars)")
				return
			}
		}
	}
	var res *store.CreateAccountResult
	if named != "" {
		r, err := s.store.CreateAccount(named, domain, false)
		if err != nil {
			if errors.Is(err, store.ErrAccountExists) {
				conflict(w, "address already taken")
				return
			}
			badRequest(w, "create subordinate: "+err.Error())
			return
		}
		res = r
	} else {
		// Random bot-<8hex> name, retried on (unlikely) collision.
		for i := 0; i < 5; i++ {
			name := "bot-" + store.GeneratePassword(8)
			r, err := s.store.CreateAccount(name, domain, false)
			if err == nil {
				res = r
				break
			}
			if !strings.Contains(err.Error(), "exists") {
				badRequest(w, "create subordinate: "+err.Error())
				return
			}
		}
	}
	if res == nil {
		internalError(w, "could not allocate a subordinate name, retry")
		return
	}

	// Declare the new account under the caller (fresh account always exists).
	if err := s.store.DeclareSubordinate(owner, res.Address); err != nil {
		// Account exists but the edge failed: unusable half-state — best we
		// can do is report; the orphaned account TTLs out of use naturally.
		internalError(w, "subordinate created but declare failed: "+err.Error())
		return
	}
	_ = s.audit.Record(r.Context(), audit.ActionSubDeclare, owner, "register-subordinate "+res.Address)
	writeJSON(w, http.StatusCreated, map[string]any{
		"address":  res.Address,
		"password": res.Password, // one-time, never stored in the clear again
		"declared": true,
	})
}
