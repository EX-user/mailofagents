package server

// Boards API (kanban/whiteboard) — v1.3 as frozen in the mail chain
// (alice's v1.2 eight-point final table + the three v1.3 pins), boss-ruled
// parameters: single full-power code by default (split_codes=true for a
// read/write pair), 20MB per-board content quota, 200 boards per account,
// no time decay, no global board cap.
//
// The code in the URL path IS the credential for board operations (share
// links, no Basic auth) — reads/appends/meta. Account auth is required only
// where ownership matters: create, mine, preamble rewrite, delete. seq is a
// server-internal counter and never leaves the server (v1.3: after_seq is
// dead; increments read via ?latest=N, ?match= and the owner-approved
// ?after=<content anchor>).
//
//   POST   /api/boards                    {name, line_count?, split_codes?}    (account)
//   GET    /api/boards/mine                                                    (account)
//   GET    /api/boards/info               limits/defaults/rates self-description (public)
//   GET    /api/boards/{code}             no-param=preamble; ?part=full | ?latest=N | ?match=kw
//   GET    /api/boards/{code}/meta        lines/bytes/created/preamble
//   POST   /api/boards/{code}/lines       {body} append (write power; rate-limited)
//   POST   /api/boards/{code}/preamble    {body} overwrite (creator + write power)
//   DELETE /api/boards/{code}                                                  (creator or admin)
//   GET    /api/admin/boards              paginated meta-only (?page=&size=)   (admin)
//
// Rate limits (v1.2 point 6): authenticated append 10 lines/min/account;
// per-code 10 lines/min/code; board ceiling 30 lines/min/board.

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/agentmail/agentmail/internal/store"
)

// append rate limits, per minute (v1.2 point 6).
const (
	boardAcctAppendPerMin  = 10
	boardCodeAppendPerMin  = 10
	boardBoardAppendPerMin = 30
)

// handleBoardsInfo serves the public self-description (v1.2 point 3):
// limits, defaults and rate rules for agents probing the feature without
// reading this file.
func (s *Server) handleBoardsInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"feature": "boards",
		"codes": map[string]any{
			"default":     "one full-power code (read+write)",
			"seed":        "optional header_row/content fields on create — creator seeds preamble+first lines with their own write power",
			"split_codes": "POST create with split_codes=true for a read/write pair",
			"auth_model":  "the code in the URL path is the credential; no Basic auth for reads/appends",
		},
		"limits": map[string]any{
			"line_max_runes":       store.BoardLineMaxRunes,
			"preamble_max_runes":   store.BoardPreambleMaxRunes,
			"line_count_default":   store.BoardLineCountDefault,
			"line_count_max":       store.BoardLineCountMax,
			"board_bytes_max":      store.BoardBytesMax,
			"boards_per_account":   store.BoardsPerAccount,
			"board_name_max_runes": store.BoardNameMaxRunes,
		},
		"rate_limits": map[string]any{
			"append_per_minute_per_account": boardAcctAppendPerMin,
			"append_per_minute_per_code":    boardCodeAppendPerMin,
			"append_per_minute_per_board":   boardBoardAppendPerMin,
		},
		"operators": map[string]any{
			"default":      "preamble + board meta only",
			"part=full":    "all retained content lines (seq ascending)",
			"latest=N":     "last N content lines (ascending)",
			"match=kw":     "case-insensitive substring filter on content lines; stacks with part=full/latest=N",
			"after=anchor": "content after the last line containing anchor (case-insensitive); stacks with match/latest; miss on an intact board = empty + anchor=not_found, miss on a rolled board = full content + anchor=rolled_past",
		},
		"seq": "server-internal monotonic counter, never returned to clients",
	})
}

// handleBoardCreate POSTs a new board for the authenticated account.
func (s *Server) handleBoardCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		Name       string   `json:"name"`
		LineCount  int      `json:"line_count"`
		SplitCodes bool     `json:"split_codes"`
		HeaderRow  string   `json:"header_row"`
		Content    []string `json:"content"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := decodeJSON(r, &req); err != nil {
		badRequest(w, "invalid body: "+err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		badRequest(w, "name is required")
		return
	}
	if utf8.RuneCountInString(name) > store.BoardNameMaxRunes {
		badRequest(w, "name too long")
		return
	}
	lineCount := req.LineCount
	if lineCount == 0 {
		lineCount = store.BoardLineCountDefault
	}
	if lineCount < 0 || lineCount > store.BoardLineCountMax {
		badRequest(w, "line_count out of range")
		return
	}
	// Optional initial seeding (owner-approved): header_row/content let the
	// creator use their own write power at create time — semantically one
	// preamble write plus one append per line. Everything is validated
	// BEFORE the board exists so the seed either happens whole or the
	// create fails with a 400 and no board lingers.
	var preamble string
	var seed []string
	if req.HeaderRow != "" {
		if strings.ContainsAny(req.HeaderRow, "\n\r") {
			badRequest(w, "header_row must be a single line")
			return
		}
		if utf8.RuneCountInString(req.HeaderRow) > store.BoardPreambleMaxRunes {
			badRequest(w, "header_row too long")
			return
		}
		preamble = req.HeaderRow
	}
	if len(req.Content) > 0 {
		total := 0
		for i, line := range req.Content {
			if strings.ContainsAny(line, "\n\r") {
				badRequest(w, fmt.Sprintf("content[%d] must be a single line", i))
				return
			}
			n := utf8.RuneCountInString(line)
			if n == 0 {
				badRequest(w, fmt.Sprintf("content[%d] is empty", i))
				return
			}
			if n > store.BoardLineMaxRunes {
				badRequest(w, fmt.Sprintf("content[%d] too long", i))
				return
			}
			total += len(line)
		}
		if int64(total) > store.BoardBytesMax {
			badRequest(w, "content exceeds the board quota")
			return
		}
		seed = req.Content
	}
	board, err := s.store.CreateBoard(accountFrom(r.Context()), name, lineCount, req.SplitCodes)
	if err != nil {
		if errors.Is(err, store.ErrBoardQuota) {
			http.Error(w, "board quota reached", http.StatusConflict)
			return
		}
		internalError(w, "create board: "+err.Error())
		return
	}
	// Seeding cannot fail here: shapes and the byte budget were checked
	// above, and rolling absorbs over-cap line counts by design.
	if preamble != "" {
		if err := s.store.SetBoardPreamble(board.ID, preamble); err != nil {
			internalError(w, "seed preamble: "+err.Error())
			return
		}
	}
	now := time.Now()
	creator := accountFrom(r.Context())
	for _, line := range seed {
		if err := s.store.AppendBoardLine(board.ID, line, creator, now); err != nil {
			internalError(w, "seed content: "+err.Error())
			return
		}
	}
	resp := map[string]any{
		"mode":       map[bool]string{true: "single", false: "split"}[board.SingleCode],
		"name":       board.Name,
		"line_count": board.LineCount,
		"created":    board.CreatedAt,
	}
	if preamble != "" {
		resp["header_row"] = true
	}
	if len(seed) > 0 {
		resp["seeded"] = len(seed)
	}
	if board.SingleCode {
		resp["code"] = board.ReadCode
	} else {
		resp["read_code"] = board.ReadCode
		resp["write_code"] = board.WriteCode
	}
	writeJSON(w, http.StatusCreated, resp)
}

// handleBoardsMine lists the caller's boards with quota usage.
func (s *Server) handleBoardsMine(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	boards, err := s.store.BoardsByOwner(accountFrom(r.Context()))
	if err != nil {
		internalError(w, "list boards: "+err.Error())
		return
	}
	out := make([]map[string]any, 0, len(boards))
	for i := range boards {
		out = append(out, boardOwnerView(&boards[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"boards": out,
		"used":   len(out),
		"max":    store.BoardsPerAccount,
	})
}

// boardOwnerView is the /mine entry: the owner sees their own codes.
func boardOwnerView(b *store.Board) map[string]any {
	v := map[string]any{
		"name":       b.Name,
		"mode":       map[bool]string{true: "single", false: "split"}[b.SingleCode],
		"line_count": b.LineCount,
		"lines":      b.Lines,
		"bytes":      b.Bytes,
		"created":    b.CreatedAt,
		"preamble":   b.Preamble,
	}
	if b.SingleCode {
		v["code"] = b.ReadCode
	} else {
		v["read_code"] = b.ReadCode
		v["write_code"] = b.WriteCode
	}
	return v
}

// handleBoardCode dispatches /api/boards/{code}[/meta|/lines|/preamble].
// Reads and appends authenticate by the path code alone; ownership
// operations re-check account credentials inside.
func (s *Server) handleBoardCode(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/boards/")
	parts := strings.Split(rest, "/")
	if len(parts) < 1 || parts[0] == "" {
		http.Error(w, "no such board", http.StatusNotFound)
		return
	}
	code := parts[0]
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			s.handleBoardRead(w, r, code)
		case http.MethodDelete:
			s.handleBoardDelete(w, r, code)
		default:
			methodNotAllowed(w)
		}
		return
	}
	if len(parts) != 2 {
		http.Error(w, "no such board", http.StatusNotFound)
		return
	}
	switch parts[1] {
	case "meta":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		s.handleBoardMeta(w, r, code)
	case "lines":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		s.handleBoardAppend(w, r, code)
	case "preamble":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		s.handleBoardPreamble(w, r, code)
	case "config":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		s.handleBoardConfig(w, r, code)
	default:
		http.Error(w, "no such board", http.StatusNotFound)
	}
}

// boardByReadCode resolves the path code for a read-ish operation; any
// code with read power passes (a full-power code has both). Writes 404 for
// unknown codes, 403 for a write-only code, and returns nil on failure.
func (s *Server) boardByReadCode(w http.ResponseWriter, code string) (*store.Board, bool) {
	board, ref, err := s.store.BoardByCode(code)
	if err != nil {
		http.Error(w, "no such board", http.StatusNotFound)
		return nil, false
	}
	if !ref.Read && !ref.Write {
		http.Error(w, "code has no read power", http.StatusForbidden)
		return nil, false
	}
	return board, true
}

// handleBoardRead GETs board meta and, per the query operators, content.
func (s *Server) handleBoardRead(w http.ResponseWriter, r *http.Request, code string) {
	board, ok := s.boardByReadCode(w, code)
	if !ok {
		return
	}
	q := r.URL.Query()
	// v1.3 pin ②, owner-approved: ?after=<content anchor> — substring
	// match (case-insensitive), multiple hits take the last, content is
	// what follows it. A miss resolves per the board's roll state.
	after := ""
	if q.Has("after") {
		after = q.Get("after")
		if strings.TrimSpace(after) == "" {
			badRequest(w, "after anchor must be non-empty")
			return
		}
	}
	base := map[string]any{
		"name":       board.Name,
		"preamble":   board.Preamble,
		"created":    board.CreatedAt,
		"line_count": board.LineCount,
		"lines":      board.Lines,
		"bytes":      board.Bytes,
		"config":     boardConfigView(board),
	}
	partFull := q.Get("part") == "full"
	latest := 0
	if raw := q.Get("latest"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			badRequest(w, "latest must be a positive integer")
			return
		}
		latest = n
	}
	match := q.Get("match")
	if !partFull && latest == 0 && match == "" && after == "" {
		writeJSON(w, http.StatusOK, base)
		return
	}
	lines, anchor, err := s.store.BoardLines(board.ID, after, match, latest)
	if err != nil {
		internalError(w, "read lines: "+err.Error())
		return
	}
	if after != "" {
		base["anchor"] = string(anchor)
	}
	// Field-level visibility (boss ruling): the panel toggles control
	// whether at/by are delivered at all — gated fields are omitted from
	// the response, not zeroed. Stored data is untouched.
	content := make([]map[string]any, 0, len(lines))
	for _, l := range lines {
		m := map[string]any{"body": l.Body}
		if board.ShowTime {
			m["at"] = l.At
		}
		if board.ShowBy {
			m["by"] = l.By
		}
		content = append(content, m)
	}
	base["content"] = content
	writeJSON(w, http.StatusOK, base)
}

// handleBoardMeta GETs the single-board self-description (v1.2 point 3).
func (s *Server) handleBoardMeta(w http.ResponseWriter, r *http.Request, code string) {
	board, ok := s.boardByReadCode(w, code)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":       board.Name,
		"preamble":   board.Preamble,
		"created":    board.CreatedAt,
		"line_count": board.LineCount,
		"lines":      board.Lines,
		"bytes":      board.Bytes,
		"config":     boardConfigView(board),
	})
}

// handleBoardAppend POSTs one content line via the write power. Anonymous
// (code-only) appends are the normal case; a valid account credential
// alongside the code adds the per-account window on top of the code and
// board ceilings.
func (s *Server) handleBoardAppend(w http.ResponseWriter, r *http.Request, code string) {
	board, ref, err := s.store.BoardByCode(code)
	if err != nil {
		http.Error(w, "no such board", http.StatusNotFound)
		return
	}
	if !ref.Write {
		http.Error(w, "code has no write power", http.StatusForbidden)
		return
	}
	var req struct {
		Body string `json:"body"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := decodeJSON(r, &req); err != nil {
		badRequest(w, "invalid body: "+err.Error())
		return
	}
	body := strings.TrimRight(req.Body, "\n")
	if strings.ContainsAny(body, "\n\r") {
		badRequest(w, "body must be a single line (a single trailing \n is trimmed; embedded newlines and \r are rejected)")
		return
	}
	if utf8.RuneCountInString(body) == 0 {
		badRequest(w, "body is required")
		return
	}
	if utf8.RuneCountInString(body) > store.BoardLineMaxRunes {
		badRequest(w, "line too long")
		return
	}
	if board.Muted {
		// Mute freezes everyone but the creator (ruling: the owner keeps
		// the floor — otherwise even a "board is muted" notice couldn't
		// be posted without unfreezing).
		acct := s.optionalAccount(r)
		if acct == "" || !strings.EqualFold(acct, board.Owner) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "board is muted"})
			return
		}
	}
	now := time.Now()
	// The per-account window only applies when the request actually
	// carries a valid account credential (optional on this endpoint — the
	// code is the credential; an empty key must never gate anonymous
	// appends as one global bucket).
	if acct := s.optionalAccount(r); acct != "" {
		if !s.boardAcctRate.allow(acct, boardAcctAppendPerMin, now) {
			http.Error(w, "board append rate limit exceeded (per account)", http.StatusTooManyRequests)
			return
		}
	}
	if !s.boardCodeRate.allow(code, boardCodeAppendPerMin, now) {
		http.Error(w, "board append rate limit exceeded (per code)", http.StatusTooManyRequests)
		return
	}
	if !s.boardBoardRate.allow(board.ID, boardBoardAppendPerMin, now) {
		http.Error(w, "board append rate limit exceeded (per board)", http.StatusTooManyRequests)
		return
	}
	if err := s.store.AppendBoardLine(board.ID, body, s.optionalAccount(r), now); err != nil {
		if errors.Is(err, store.ErrBoardFull) {
			http.Error(w, "board content quota exceeded", http.StatusRequestEntityTooLarge)
			return
		}
		internalError(w, "append line: "+err.Error())
		return
	}
	// seq stays server-side by design (v1.3): the client needs no ordering
	// token — reads compose operators on content instead.
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleBoardPreamble overwrites the preamble: write power in the path AND
// the authenticated account must be the board's creator (v1.2 point 5).
func (s *Server) handleBoardPreamble(w http.ResponseWriter, r *http.Request, code string) {
	board, ref, err := s.store.BoardByCode(code)
	if err != nil {
		http.Error(w, "no such board", http.StatusNotFound)
		return
	}
	if !ref.Write {
		http.Error(w, "code has no write power", http.StatusForbidden)
		return
	}
	address, ok := s.authAccount(w, r)
	if !ok {
		return // 401 written
	}
	if !strings.EqualFold(address, board.Owner) {
		http.Error(w, "only the creator can rewrite the preamble", http.StatusForbidden)
		return
	}
	var req struct {
		Body string `json:"body"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := decodeJSON(r, &req); err != nil {
		badRequest(w, "invalid body: "+err.Error())
		return
	}
	body := strings.TrimSpace(req.Body)
	if body == "" || strings.ContainsAny(body, "\n\r") {
		badRequest(w, "preamble must be a single non-empty line")
		return
	}
	if utf8.RuneCountInString(body) > store.BoardPreambleMaxRunes {
		badRequest(w, "preamble too long")
		return
	}
	if err := s.store.SetBoardPreamble(board.ID, body); err != nil {
		internalError(w, "set preamble: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "preamble": body})
}

// boardConfigView is the creator-toggleable display/mute configuration.
func boardConfigView(b *store.Board) map[string]any {
	return map[string]any{
		"show_time": b.ShowTime,
		"show_by":   b.ShowBy,
		"muted":     b.Muted,
	}
}

// handleBoardConfig POSTs a partial config update: creator-only with the
// write code in the path (preamble three-gate pattern). Keys not present
// in the body stay unchanged; toggles are render-only and never rewrite
// stored lines.
func (s *Server) handleBoardConfig(w http.ResponseWriter, r *http.Request, code string) {
	board, ref, err := s.store.BoardByCode(code)
	if err != nil {
		http.Error(w, "no such board", http.StatusNotFound)
		return
	}
	if !ref.Write {
		http.Error(w, "code has no write power", http.StatusForbidden)
		return
	}
	address, ok := s.authAccount(w, r)
	if !ok {
		return // 401 written
	}
	if !strings.EqualFold(address, board.Owner) {
		http.Error(w, "only the creator can change board config", http.StatusForbidden)
		return
	}
	var req struct {
		ShowTime *bool `json:"show_time"`
		ShowBy   *bool `json:"show_by"`
		Muted    *bool `json:"muted"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := decodeJSON(r, &req); err != nil {
		badRequest(w, "invalid body: "+err.Error())
		return
	}
	if req.ShowTime == nil && req.ShowBy == nil && req.Muted == nil {
		badRequest(w, "nothing to update")
		return
	}
	if err := s.store.SetBoardConfig(board.ID, req.ShowTime, req.ShowBy, req.Muted); err != nil {
		internalError(w, "set config: "+err.Error())
		return
	}
	fresh, err := s.store.BoardByID(board.ID)
	if err != nil {
		internalError(w, "reload board: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": boardConfigView(fresh)})
}

// handleBoardDelete removes a board: creator or admin only (v1.2 point on
// delete). The path code identifies the board; it does not authorize the
// delete by itself.
func (s *Server) handleBoardDelete(w http.ResponseWriter, r *http.Request, code string) {
	board, _, err := s.store.BoardByCode(code)
	if err != nil {
		http.Error(w, "no such board", http.StatusNotFound)
		return
	}
	address, ok := s.authAccount(w, r)
	if !ok {
		return // 401 written
	}
	if !strings.EqualFold(address, board.Owner) && !s.callerIsAdmin(r) {
		http.Error(w, "only the creator or an admin can delete a board", http.StatusForbidden)
		return
	}
	if err := s.store.DeleteBoard(board.ID); err != nil {
		internalError(w, "delete board: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAdminBoards GETs one page of the admin board list: meta only
// (name/owner/bytes/lines/created), never codes or preamble (v1.2 point 2 —
// governance census, not content access).
func (s *Server) handleAdminBoards(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	page := queryInt(r, "page", 1)
	if page < 1 {
		page = 1
	}
	size := queryInt(r, "size", 50)
	if size < 1 {
		size = 50
	}
	if size > 200 {
		size = 200
	}
	boards, total, err := s.store.AdminBoardPage(page, size)
	if err != nil {
		internalError(w, "admin boards: "+err.Error())
		return
	}
	out := make([]map[string]any, 0, len(boards))
	for i := range boards {
		b := &boards[i]
		out = append(out, map[string]any{
			"name":    b.Name,
			"owner":   b.Owner,
			"bytes":   b.Bytes,
			"lines":   b.Lines,
			"created": b.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"boards": out,
		"page":   page,
		"size":   size,
		"total":  total,
	})
}

// callerIsAdmin is basicAuthAdmin without the response writes, for paths
// where admin is one accepted role among others rather than the gate.
func (s *Server) callerIsAdmin(r *http.Request) bool {
	if tok := bearerToken(r.Header.Get("Authorization")); tok != "" {
		addr, err := s.store.ResolveSessionToken(tok)
		if err != nil {
			return false
		}
		acc, err := s.store.GetAccount(addr)
		return err == nil && acc.IsAdmin
	}
	user, pass, ok := parseBasicAuth(r.Header.Get("Authorization"))
	if !ok {
		return false
	}
	acc, err := s.store.GetAccount(user)
	if err != nil || bcryptCompare(acc.PasswordHash, []byte(pass)) != nil {
		return false
	}
	return acc.IsAdmin
}

// optionalAccount resolves the request's account credential without ever
// writing a 401: board reads/appends authenticate by path code, so a
// missing or invalid account credential is simply "no account window".
func (s *Server) optionalAccount(r *http.Request) string {
	if tok := bearerToken(r.Header.Get("Authorization")); tok != "" {
		addr, err := s.store.ResolveSessionToken(tok)
		if err != nil {
			return ""
		}
		return addr
	}
	user, pass, ok := parseBasicAuth(r.Header.Get("Authorization"))
	if !ok || s.store.VerifyPassword(user, pass) != nil {
		return ""
	}
	return user
}
