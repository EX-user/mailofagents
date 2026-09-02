package server

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/agentmail/agentmail/internal/audit"
	"github.com/agentmail/agentmail/internal/store"
)

// accountView is the admin-facing projection of an account: the password hash
// is stripped so it can never leak via the API.
type accountView struct {
	UUID      string `json:"uuid"`
	Address   string `json:"address"`
	IsAdmin   bool   `json:"is_admin"`
	Disabled  bool   `json:"disabled"`
	Visible   bool   `json:"visible"`
	Signature string `json:"signature"`
	CreatedAt int64  `json:"created_at"`
}

// handleAdminMessages lets the admin read any account's inbox.
//
//	GET /admin/messages?account=<addr>&limit=50
//	-> {"account": "...", "messages": [...], "count": N}
func (s *Server) handleAdminMessages(w http.ResponseWriter, r *http.Request) {
	account := strings.TrimSpace(r.URL.Query().Get("account"))
	if account == "" {
		badRequest(w, "account query parameter required")
		return
	}
	limit := queryInt(r, "limit", 50)
	// admin viewing another's inbox: show the OWNER's unread state (so admin can
	// see whether the owner has read these messages), not admin's own (which
	// would always be "read" since admin isn't a recipient).
	msgs, err := s.store.ReadInbox(account, limit)
	if err != nil {
		if errors.Is(err, store.ErrAccountNotFound) {
			http.Error(w, "account not found", http.StatusNotFound)
			return
		}
		internalError(w, "read inbox: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"account":  account,
		"messages": msgs,
		"count":    len(msgs),
	})
}

// handleAdminAudit returns recent audit entries.
//
//	GET /admin/audit?limit=100  -> {"entries": [...], "count": N}
func (s *Server) handleAdminAudit(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 100)
	entries, err := s.audit.List(r.Context(), limit)
	if err != nil {
		internalError(w, "audit list: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "count": len(entries)})
}

// handleAdminAccounts lists every account WITHOUT password hashes.
//   GET /admin/accounts  -> {"accounts": [...], "count": N}

// dedupeAccountsByLowerKey merges physical rows that differ only by key
// casing (legacy mixed-case rows, superior ruling: data stays, the RETURN
// layer merges). The canonical lowercase-keyed row wins; otherwise the
// first-seen row is kept with its address shown lowercased.
func dedupeAccountsByLowerKey[T any](rows []T, addr func(T) string, setAddr func(*T)) []T {
	byLower := map[string]int{}
	out := make([]T, 0, len(rows))
	for _, r := range rows {
		l := strings.ToLower(addr(r))
		if i, ok := byLower[l]; ok {
			if addr(rows[i]) != l && addr(r) == l {
				rows[i] = r // prefer the true lowercase-keyed row
			}
			continue
		}
		byLower[l] = len(out)
		setAddr(&r)
		out = append(out, r)
	}
	return out
}

func (s *Server) handleAdminAccounts(w http.ResponseWriter, r *http.Request) {
	accs, err := s.store.ListAccounts()
	if err != nil {
		internalError(w, "list accounts: "+err.Error())
		return
	}
	// Return-layer merge: legacy mixed-case twin rows collapse into their
	// lowercase twin (data untouched — superior ruling).
	type row = store.Account
	rows := dedupeAccountsByLowerKey(accs,
		func(a row) string { return a.Address },
		func(a *row) { a.Address = strings.ToLower(a.Address) })
	out := make([]accountView, 0, len(rows))
	for _, a := range rows {
		out = append(out, accountView{
			UUID:      a.UUID,
			Address:   a.Address,
			IsAdmin:   a.IsAdmin,
			Disabled:  a.Disabled,
			Visible:   a.Visible,
			Signature: a.Signature,
			CreatedAt: a.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": out, "count": len(out)})
}

// handleAdminSent lets the admin read any account's sent folder.
//
//	GET /admin/sent?account=<addr>&limit=50
//	-> {"account": "...", "messages": [...], "count": N}
func (s *Server) handleAdminSent(w http.ResponseWriter, r *http.Request) {
	account := strings.TrimSpace(r.URL.Query().Get("account"))
	if account == "" {
		badRequest(w, "account query parameter required")
		return
	}
	limit := queryInt(r, "limit", 50)
	msgs, err := s.store.ReadSent(account, limit)
	if err != nil {
		if errors.Is(err, store.ErrAccountNotFound) {
			http.Error(w, "account not found", http.StatusNotFound)
			return
		}
		internalError(w, "read sent: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"account":  account,
		"messages": msgs,
		"count":    len(msgs),
	})
}

// handleAdminMessage returns the full body of any message by ID, bypassing the
// per-account visibility check (the admin can read anything).
//
//	GET /admin/message?id=...  -> {"id","from","to","subject","body","received_at"}
func (s *Server) handleAdminMessage(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		badRequest(w, "id is required")
		return
	}
	msg, err := s.store.GetMessageAdmin(id)
	if err != nil {
		if errors.Is(err, store.ErrMessageNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		internalError(w, "get message: "+err.Error())
		return
	}
	// If the admin is an actual recipient of this message, mark it read for
	// the admin (so the admin's own inbox unread state stays accurate). When
	// the admin is merely viewing someone else's mail, do NOT mutate state.
	for _, rcpt := range msg.To {
		if rcpt == s.adminAddress() {
			if acc, err := s.store.GetAccount(s.adminAddress()); err == nil {
				_ = s.store.MarkRead(acc.UUID, id)
			}
			break
		}
	}
	resp := map[string]any{
		"id":          msg.ID,
		"from":        msg.From,
		"to":          msg.To,
		"in_reply_to": msg.InReplyTo,
		"subject":     msg.Subject,
		"body":        msg.Body,
		"received_at": msg.ReceivedAt,
	}
	if len(msg.CC) > 0 {
		resp["cc"] = msg.CC
	}
	if len(msg.Attachments) > 0 {
		type attOut struct {
			store.AttachmentMeta
			ExpiresAt int64 `json:"expires_at"`
		}
		out := make([]attOut, 0, len(msg.Attachments))
		for _, a := range msg.Attachments {
			out = append(out, attOut{AttachmentMeta: a, ExpiresAt: s.store.AttachmentExpiresAt(a)})
		}
		resp["attachments"] = out
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleAdminThread returns the bilateral conversation between an arbitrary
// account and a peer (admin view).
//
//	GET /admin/thread?account=X&with=Y&limit=50&offset=0
func (s *Server) handleAdminThread(w http.ResponseWriter, r *http.Request) {
	account := strings.TrimSpace(r.URL.Query().Get("account"))
	peer := strings.TrimSpace(r.URL.Query().Get("with"))
	if account == "" || peer == "" {
		badRequest(w, "account and with are required")
		return
	}
	limit := queryInt(r, "limit", 50)
	offset := queryInt(r, "offset", 0)
	entries, err := s.store.ReadThread(account, peer, limit, offset)
	if err != nil {
		if errors.Is(err, store.ErrAccountNotFound) {
			http.Error(w, "account not found", http.StatusNotFound)
			return
		}
		internalError(w, "read thread: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"account":  account,
		"peer":     peer,
		"messages": entries,
		"count":    len(entries),
	})
}

// handleAdminMessagesAll returns the newest messages across all accounts
// in ONE request (replaces the per-account fan-out that saturated the
// server on the admin Mail page's "All accounts" view).
//
//	GET /admin/messages-all?limit=50&folder=all|inbox|sent
//	  -> {"messages": [...MessageSummary], "count": N, "total_count": T}
func (s *Server) handleAdminMessagesAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	limit := queryInt(r, "limit", 50)
	folder := strings.TrimSpace(r.URL.Query().Get("folder"))
	msgs, total, err := s.store.ReadAllAccountsMessages(folder, limit)
	if err != nil {
		internalError(w, "read all messages: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"messages":    msgs,
		"count":       len(msgs),
		"total_count": total,
	})
}

// handleAdminStats returns overall counts for the overview page.
//
//	GET /admin/stats  -> {"accounts": N, "messages": N}
func (s *Server) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	accN, err := s.store.CountAccounts()
	if err != nil {
		internalError(w, "count accounts: "+err.Error())
		return
	}
	msgN, err := s.store.CountMessages()
	if err != nil {
		internalError(w, "count messages: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": accN, "messages": msgN})
}

// handleAdminResetPassword sets a new password for any account (the admin's
// own included). The new password is returned in plaintext exactly once so the
// admin can hand it to the account owner.
//
//	POST /admin/reset-password  {"account": "<addr>", "new_password": "<optional>"}
//	  -> {"account": "...", "password": "<the new plaintext password>"}
//
// If new_password is omitted or empty, a random 24-char password is generated.
// A supplied new_password must be at least 8 chars. The old password is NOT
// verified (the admin has already authenticated).
func (s *Server) handleAdminResetPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body struct {
		Account     string `json:"account"`
		NewPassword string `json:"new_password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, "invalid body: "+err.Error())
		return
	}
	account := strings.TrimSpace(body.Account)
	if account == "" {
		badRequest(w, "account is required")
		return
	}

	password := strings.TrimSpace(body.NewPassword)
	if password == "" {
		// Generate a random one when the admin did not supply one.
		password = store.GeneratePassword(24)
	} else if len(password) < 8 {
		badRequest(w, "new_password must be at least 8 characters")
		return
	}

	if err := s.store.ResetPassword(account, password); err != nil {
		if errors.Is(err, store.ErrAccountNotFound) {
			http.Error(w, "account not found", http.StatusNotFound)
			return
		}
		internalError(w, "reset password: "+err.Error())
		return
	}
	detail := "by=admin"
	if strings.TrimSpace(body.NewPassword) == "" {
		detail += " random=true"
	} else {
		detail += " random=false"
	}
	_ = s.audit.Record(r.Context(), audit.ActionResetPassword, account, detail)
	writeJSON(w, http.StatusOK, map[string]any{
		"account":  account,
		"password": password,
	})
}

// handleAdminSetDisabled toggles an account's disabled flag. A disabled account
// cannot authenticate (so it can neither send nor read mail), but the account
// and its message history persist. The admin cannot disable itself (lockout
// guard). Reversible by calling with disabled=false.
//
//	POST /admin/set-disabled  {"account": "<addr>", "disabled": true|false}
//	  -> {"account": "...", "disabled": bool}
func (s *Server) handleAdminSetDisabled(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body struct {
		Account  string `json:"account"`
		Disabled bool   `json:"disabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, "invalid body: "+err.Error())
		return
	}
	account := strings.TrimSpace(body.Account)
	if account == "" {
		badRequest(w, "account is required")
		return
	}
	// Lockout guard: the admin cannot disable itself.
	if account == s.adminAddress() && body.Disabled {
		badRequest(w, "cannot disable your own admin account")
		return
	}
	if err := s.store.SetAccountDisabled(account, body.Disabled); err != nil {
		if errors.Is(err, store.ErrAccountNotFound) {
			http.Error(w, "account not found", http.StatusNotFound)
			return
		}
		internalError(w, "set disabled: "+err.Error())
		return
	}
	state := "enable"
	if body.Disabled {
		state = "disable"
	}
	_ = s.audit.Record(r.Context(), audit.ActionDisableAccount, account, "by=admin "+state)
	writeJSON(w, http.StatusOK, map[string]any{
		"account":  account,
		"disabled": body.Disabled,
	})
}

// handleAdminSend lets the admin send mail as the admin account. The from
// address is forced to the configured admin address (the admin cannot spoof
// another sender), so the admin speaks only as itself.
//
//	POST /admin/send  {"to": [...], "subject": "...", "body": "..."}
//	  -> {"message_id": "...", "status": "sent"}
func (s *Server) handleAdminSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body struct {
		To          []string `json:"to"`
		CC          []string `json:"cc"`
		Subject     string   `json:"subject"`
		Body        string   `json:"body"`
		InReplyTo   string   `json:"in_reply_to"`
		Attachments []string `json:"attachments"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, "invalid body: "+err.Error())
		if body.InReplyTo != "" && !isULID(body.InReplyTo) {
			badRequest(w, "in_reply_to must be a 26-char ULID")
			return
		}
		return
	}
	if len(body.To) == 0 {
		badRequest(w, "to is required")
		return
	}
	if body.Subject == "" || body.Body == "" {
		badRequest(w, "subject and body are required")
		return
	}

	// Rate limits apply to admin too.
	from := s.adminAddress()
	if err := s.checkSendRate(from); err != nil {
		http.Error(w, err.Error(), http.StatusTooManyRequests)
		return
	}
	bodyLen := int64(len(body.Body))
	var validRecipients []string
	for _, rcpt := range append(append([]string{}, body.To...), body.CC...) {
		if s.checkRecvRate(rcpt, bodyLen) {
			validRecipients = append(validRecipients, rcpt)
		}
	}
	if len(validRecipients) == 0 {
		http.Error(w, "all recipients exceeded byte rate limit", http.StatusTooManyRequests)
		return
	}

	fromName := localPart(from)
	var res *store.SendResult
	var err error
	if len(body.Attachments) > 0 {
		// API parity with /api/send: attachments must be the admin's own
		// uploads; the store validates ownership and grants recipients.
		res, err = s.store.SendWithAttachments(from, fromName, body.To, body.CC, body.Subject, body.Body, body.Attachments, body.InReplyTo)
	} else {
		res, err = s.store.Send(from, fromName, body.To, body.CC, body.Subject, body.Body, body.InReplyTo)
	}
	if err != nil {
		// Sentinel first (user-facing), then generic+log (weber ③).
		if errors.Is(err, store.ErrNoSuchParent) {
			badRequest(w, err.Error())
			return
		}
		s.badRequestErr(w, r, err)
		return
	}
	_ = s.audit.Record(r.Context(), audit.ActionSend, from,
		fmt.Sprintf("to=%s subj_len=%d", strings.Join(body.To, ","), len(body.Subject)))
	writeJSON(w, http.StatusOK, map[string]any{
		"message_id": res.MessageID,
		"status":     "sent",
	})
}

// handleAdminSettings returns the current system settings.
//
//	GET /admin/settings -> {registration_enabled, directory_listed_enabled, send_rate, byte_rate}
func (s *Server) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"registration_enabled":      s.store.IsRegistrationEnabled(),
		"directory_listed_enabled":  s.store.IsDirectoryListedEnabled(),
		"oneclick_register_enabled": s.store.IsOneclickRegisterEnabled(),
		"random_register_enabled":   s.store.IsRandomRegisterEnabled(),
		"showcase_enabled":          s.store.IsShowcaseEnabled(),
		"danmaku_mode":              s.store.GetDanmakuDefaultMode(),
		"danmaku_speed":             s.store.GetDanmakuDefaultSpeed(),
		"danmaku_count":             s.store.GetDanmakuDefaultCount(),
		"send_rate":                 s.store.GetSendRateLimit(),
		"byte_rate":                 s.store.GetByteRateLimit(),
		"register_rate":             s.store.GetRegisterIPRateLimit(),
		"files_total_limit":         s.store.GetFilesTotalLimit(),
		"file_quota_per_acct":       s.store.GetFileQuotaPerAcct(),
	})
}

// handleAdminSetRegistration toggles public registration.
//
//	POST /admin/set-registration {"enabled": bool}
func (s *Server) handleAdminSetRegistration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, "invalid body: "+err.Error())
		return
	}
	if err := s.store.SetRegistrationEnabled(body.Enabled); err != nil {
		internalError(w, "set registration: "+err.Error())
		return
	}
	state := "enable"
	if !body.Enabled {
		state = "disable"
	}
	_ = s.audit.Record(r.Context(), audit.ActionSetRegistration, "registration", "by=admin "+state)
	writeJSON(w, http.StatusOK, map[string]any{"registration_enabled": body.Enabled})
}

// handleAdminSetOneclickRegister toggles the portal's one-click register UX.
//
//	POST /admin/set-oneclick-register {"enabled": bool}
func (s *Server) handleAdminSetOneclickRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, "invalid body: "+err.Error())
		return
	}
	if err := s.store.SetOneclickRegisterEnabled(body.Enabled); err != nil {
		internalError(w, "set oneclick register: "+err.Error())
		return
	}
	state := "enable"
	if !body.Enabled {
		state = "disable"
	}
	_ = s.audit.Record(r.Context(), audit.ActionSetOneclickRegister, "oneclick_register", "by=admin "+state)
	writeJSON(w, http.StatusOK, map[string]any{"oneclick_register_enabled": body.Enabled})
}

// handleAdminSetRandomRegister toggles the retired passwordless register
// path (debug only — the public one-click entry is gone from the UI).
//
//	POST /admin/set-random-register {"enabled": bool}
func (s *Server) handleAdminSetRandomRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, "invalid body: "+err.Error())
		return
	}
	if err := s.store.SetRandomRegisterEnabled(body.Enabled); err != nil {
		internalError(w, "set random register: "+err.Error())
		return
	}
	state := "enable"
	if !body.Enabled {
		state = "disable"
	}
	_ = s.audit.Record(r.Context(), audit.ActionSetRandomRegister, "random_register", "by=admin "+state)
	writeJSON(w, http.StatusOK, map[string]any{"random_register_enabled": body.Enabled})
}

// handleAdminNormalizeAccountCase repairs pre-fix account keys stored with
// uppercase letters (the double-listing an outside user reported). Safe to
// run repeatedly — a clean store is a no-op. Back up the db first.
//
//	POST /admin/normalize-account-case  -> {already_lower, renamed, deleted_duplicates}
func (s *Server) handleAdminNormalizeAccountCase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	res, err := s.store.NormalizeAccountCase()
	if err != nil {
		internalError(w, "normalize account case: "+err.Error())
		return
	}
	_ = s.audit.Record(r.Context(), audit.ActionNormalizeAccountCase, "accounts",
		fmt.Sprintf("by=admin already_lower=%d renamed=%d deleted_dupes=%d", res.AlreadyLower, res.Renamed, res.DeletedDupes))
	writeJSON(w, http.StatusOK, res)
}

// handleAdminClearShowcase empties the showcase bucket (e.g. after bad data
// such as encoding-mangled entries landed). Real mail is untouched.
//
//	POST /admin/clear-showcase
func (s *Server) handleAdminClearShowcase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	before, _ := s.store.CountShowcase()
	if err := s.store.ClearShowcase(); err != nil {
		internalError(w, "clear showcase: "+err.Error())
		return
	}
	_ = s.audit.Record(r.Context(), audit.ActionClearShowcase, "showcase",
		fmt.Sprintf("by=admin cleared=%d", before))
	writeJSON(w, http.StatusOK, map[string]any{"cleared": before, "count": 0})
}

// handleAdminSetDanmaku sets the portal danmaku defaults (server-side
// defaults; browsers may still override locally).
//
//	POST /admin/set-danmaku {"mode":"A"|"B","speed":"slow|medium|fast","count":"few|normal|more"}
//	All fields optional; only provided fields are updated.
func (s *Server) handleAdminSetDanmaku(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body struct {
		Mode  string `json:"mode"`
		Speed string `json:"speed"`
		Count string `json:"count"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, "invalid body: "+err.Error())
		return
	}
	if err := s.store.SetDanmakuDefaults(body.Mode, body.Speed, body.Count); err != nil {
		badRequest(w, err.Error())
		return
	}
	_ = s.audit.Record(r.Context(), audit.ActionSetDanmaku, "danmaku",
		fmt.Sprintf("by=admin mode=%s speed=%s count=%s", body.Mode, body.Speed, body.Count))
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":  s.store.GetDanmakuDefaultMode(),
		"speed": s.store.GetDanmakuDefaultSpeed(),
		"count": s.store.GetDanmakuDefaultCount(),
	})
}

// handleAdminGetShowcaseItem fetches one showcase entry by id (the admin's
// search-then-delete flow).
//
//	GET /admin/showcase-item?id=... -> {id, from, subject, received_at}; 404 if absent.
//
// Body is deliberately omitted — the delete flow does not need it.
func (s *Server) handleAdminGetShowcaseItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		badRequest(w, "id is required")
		return
	}
	e, err := s.store.GetShowcaseEntry(id)
	if err != nil {
		if errors.Is(err, store.ErrShowcaseNotFound) {
			http.Error(w, "showcase entry not found", http.StatusNotFound)
			return
		}
		internalError(w, "get showcase item: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":          e.ID,
		"from":        e.From,
		"subject":     e.Subject,
		"received_at": e.ReceivedAt,
	})
}

// handleAdminDeleteShowcaseItem removes one showcase entry by id.
//
//	POST /admin/delete-showcase-item {"id": "..."} -> {ok: true}; 404 if absent.
func (s *Server) handleAdminDeleteShowcaseItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, "invalid body: "+err.Error())
		return
	}
	if strings.TrimSpace(body.ID) == "" {
		badRequest(w, "id is required")
		return
	}
	if err := s.store.DeleteShowcaseEntry(body.ID); err != nil {
		if errors.Is(err, store.ErrShowcaseNotFound) {
			http.Error(w, "showcase entry not found", http.StatusNotFound)
			return
		}
		internalError(w, "delete showcase item: "+err.Error())
		return
	}
	_ = s.audit.Record(r.Context(), audit.ActionDeleteShowcaseItem, "showcase",
		fmt.Sprintf("by=admin id=%s", body.ID))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAdminSetShowcase toggles the Compose "public showcase" checkbox UI.
// Per admin's clarification it does NOT gate the tee or the showcase
// endpoint — the portal keeps serving public mail regardless.
//
//	POST /admin/set-showcase {"enabled": bool}
func (s *Server) handleAdminSetShowcase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, "invalid body: "+err.Error())
		return
	}
	if err := s.store.SetShowcaseEnabled(body.Enabled); err != nil {
		internalError(w, "set showcase: "+err.Error())
		return
	}
	state := "enable"
	if !body.Enabled {
		state = "disable"
	}
	_ = s.audit.Record(r.Context(), audit.ActionSetShowcase, "showcase", "by=admin "+state)
	writeJSON(w, http.StatusOK, map[string]any{"showcase_enabled": body.Enabled})
}

// handleAdminSetDirectoryListed toggles whether accounts may opt themselves
// into the public directory. When disabled, existing listed accounts stay
// listed, but no new false→true transitions are allowed.
//
//	POST /admin/set-directory-listed {"enabled": bool}
func (s *Server) handleAdminSetDirectoryListed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, "invalid body: "+err.Error())
		return
	}
	if err := s.store.SetDirectoryListedEnabled(body.Enabled); err != nil {
		internalError(w, "set directory listed: "+err.Error())
		return
	}
	state := "enable"
	if !body.Enabled {
		state = "disable"
	}
	_ = s.audit.Record(r.Context(), audit.ActionSetDirectoryListed, "directory_listed", "by=admin "+state)
	writeJSON(w, http.StatusOK, map[string]any{"directory_listed_enabled": body.Enabled})
}

// handleAdminSetLimits adjusts the rate limits.
//
//	POST /admin/set-limits {"send_rate": 500, "byte_rate": 1048576, "register_rate": 5}
//	All fields optional; only provided fields are updated. register_rate is
//	the per-IP registration attempt limit per hour (0 disables).
func (s *Server) handleAdminSetLimits(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body struct {
		SendRate     *int   `json:"send_rate"`
		ByteRate     *int64 `json:"byte_rate"`
		RegisterRate *int   `json:"register_rate"`
		FilesTotal   *int64 `json:"files_total_limit"`
		FileQuota    *int64 `json:"file_quota_per_acct"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, "invalid body: "+err.Error())
		return
	}
	if body.SendRate != nil {
		if *body.SendRate < 1 {
			badRequest(w, "send_rate must be positive")
			return
		}
		if err := s.store.SetSendRateLimit(*body.SendRate); err != nil {
			internalError(w, "set send rate: "+err.Error())
			return
		}
	}
	if body.ByteRate != nil {
		if *body.ByteRate < 1 {
			badRequest(w, "byte_rate must be positive")
			return
		}
		if err := s.store.SetByteRateLimit(*body.ByteRate); err != nil {
			internalError(w, "set byte rate: "+err.Error())
			return
		}
	}
	if body.RegisterRate != nil {
		if *body.RegisterRate < 0 {
			badRequest(w, "register_rate must be >= 0 (0 disables)")
			return
		}
		if err := s.store.SetRegisterIPRateLimit(*body.RegisterRate); err != nil {
			internalError(w, "set register rate: "+err.Error())
			return
		}
	}
	if body.FilesTotal != nil {
		if *body.FilesTotal < 1<<20 {
			badRequest(w, "files_total_limit must be >= 1MB")
			return
		}
		if err := s.store.SetFilesTotalLimit(*body.FilesTotal); err != nil {
			internalError(w, "set files total limit: "+err.Error())
			return
		}
	}
	if body.FileQuota != nil {
		if *body.FileQuota < 1<<20 {
			badRequest(w, "file_quota_per_acct must be >= 1MB")
			return
		}
		if err := s.store.SetFileQuotaPerAcct(*body.FileQuota); err != nil {
			internalError(w, "set file quota: "+err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"send_rate":           s.store.GetSendRateLimit(),
		"byte_rate":           s.store.GetByteRateLimit(),
		"register_rate":       s.store.GetRegisterIPRateLimit(),
		"files_total_limit":   s.store.GetFilesTotalLimit(),
		"file_quota_per_acct": s.store.GetFileQuotaPerAcct(),
	})
}

// handleAdminInvalid lists or purges INVALID mail: messages whose TO
// recipients ALL fail account lookup at evaluation time. CC recipients are
// not counted, and a message with at least one live TO recipient (mixed
// delivery) is neither listed nor deletable. Deletion is REAL — the body
// record plus every inbox/unread/sent reference — irreversible, audited,
// and preceded by an automatic database snapshot whenever more than one
// record (or all) is removed.
//
//	GET    /admin/invalid
//	  -> [{"id","from","subject","to","received_at"}...]  (newest first)
//	DELETE /admin/invalid  {"ids": ["<ulid>", ...]}  |  {"all": true}
//	  -> {"deleted": n}
//
// The removal only ever touches messages that re-qualify as invalid inside
// the delete transaction; valid mail and normal send/receive flows are
// unaffected.
func (s *Server) handleAdminInvalid(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := s.store.ListInvalidMail()
		if err != nil {
			internalError(w, "invalid mail scan: "+err.Error())
			return
		}
		if list == nil {
			list = []store.InvalidMail{}
		}
		writeJSON(w, http.StatusOK, list)

	case http.MethodDelete:
		var body struct {
			IDs []string `json:"ids"`
			All bool     `json:"all"`
		}
		if err := decodeJSON(r, &body); err != nil {
			badRequest(w, "invalid body: "+err.Error())
			return
		}
		if !body.All && len(body.IDs) == 0 {
			badRequest(w, "ids or all is required")
			return
		}
		// Strict-mode safety gate: before a mass removal, snapshot the
		// database (consistent read-tx write, safe under live traffic) and
		// audit the snapshot itself.
		if body.All || len(body.IDs) > 1 {
			snapshot, err := s.store.BackupTimestamped()
			if err != nil {
				internalError(w, "pre-delete backup: "+err.Error())
				return
			}
			_ = s.audit.Record(r.Context(), audit.ActionInvalidMailBackup,
				"admin", "snapshot="+filepath.Base(snapshot))
		}
		n, err := s.store.DeleteInvalidMail(body.IDs, body.All)
		if err != nil {
			internalError(w, "invalid mail delete: "+err.Error())
			return
		}
		detail := fmt.Sprintf("all=%v requested=%d deleted=%d", body.All, len(body.IDs), n)
		_ = s.audit.Record(r.Context(), audit.ActionInvalidMailDelete, "admin", detail)
		writeJSON(w, http.StatusOK, map[string]any{"deleted": n})

	default:
		methodNotAllowed(w)
	}
}
