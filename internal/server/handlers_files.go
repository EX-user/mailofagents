package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/agentmail/agentmail/internal/audit"
	"github.com/agentmail/agentmail/internal/store"
)

// Attachment system endpoints (v0.5 Phase 1): upload + download.
// Management endpoints (v0.5.18, attachment management card):
//
//   POST   /api/files/upload           (multipart: file, allowed="a@x,b@y")
//     -> {"id","access_code","filename","size"}
//   GET    /api/files/{id}/download?code=...   -> raw content
//   GET    /api/files/list              -> {files:[{id,filename,size,created_at,expires_at}]}
//                                         (auth=self; expiry ascending; not-yet-swept
//                                         expired files included)
//   DELETE /api/files/{id}              -> {deleted} (own files only; immediate,
//                                         quota reclaims implicitly; sent mail's
//                                         download links 404 afterwards)
//
// Download authorization: the Basic-auth account must be the owner or on
// the file's allowed list, AND the access code must match. Wrong
// permission and wrong code both answer 404 (no oracle).

const fileUploadMaxMemory = 2 << 20 // buffer the multipart in memory (1MB cap + form overhead)

// handleFileUpload stores one file for the authenticated account.
func (s *Server) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	who := accountFrom(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, store.FileMaxBytes+64<<10)
	if err := r.ParseMultipartForm(fileUploadMaxMemory); err != nil {
		badRequest(w, "invalid multipart form: "+err.Error())
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()

	f, hdr, err := r.FormFile("file")
	if err != nil {
		badRequest(w, "file part is required")
		return
	}
	defer f.Close()

	// Enforce the per-file cap BEFORE reading the whole body into memory.
	if hdr.Size > store.FileMaxBytes {
		http.Error(w, fmt.Sprintf("file too large: %d bytes (limit %d)", hdr.Size, store.FileMaxBytes), http.StatusRequestEntityTooLarge)
		return
	}
	content, err := io.ReadAll(io.LimitReader(f, store.FileMaxBytes+1))
	if err != nil {
		badRequest(w, "read file: "+err.Error())
		return
	}
	if int64(len(content)) > store.FileMaxBytes {
		http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
		return
	}

	name := sanitizeFilename(hdr.Filename)
	var allowed []string
	for _, a := range strings.Split(r.FormValue("allowed"), ",") {
		if a = strings.TrimSpace(a); a != "" {
			allowed = append(allowed, strings.ToLower(a))
		}
	}

	rec, err := s.store.SaveFile(who, name, allowed, content)
	if err != nil {
		if errors.Is(err, store.ErrQuotaExceeded) {
			http.Error(w, "storage quota exceeded", http.StatusRequestEntityTooLarge)
			return
		}
		internalError(w, "save file: "+err.Error())
		return
	}
	_ = s.audit.Record(r.Context(), audit.ActionFileUpload, who,
		fmt.Sprintf("id=%s name=%s size=%d allowed=%d", rec.ID, name, rec.Size, len(allowed)))
	writeJSON(w, http.StatusOK, map[string]any{
		"id":          rec.ID,
		"access_code": rec.AccessCode,
		"filename":    rec.Filename,
		"size":        rec.Size,
	})
}

// handleFileDownload streams a file's content to an authorized account.
// extendRateLimit caps attachment renewals per account per hour (the
// window is hourly — stricter than a per-minute cap but ample for
// interactive management).
const extendRateLimit = 10

// allowFileOp counts management-card mutations per account per hour
// (extend today; delete can join the same window if it ever needs a cap).
func (s *Server) allowFileOp(account string, limit int) bool {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	now := time.Now()
	rw := s.fileRates[account]
	if rw == nil || now.Sub(rw.windowStart) >= time.Hour {
		rw = &rateWindow{windowStart: now}
		s.fileRates[account] = rw
	}
	if rw.count >= limit {
		return false
	}
	rw.count++
	return true
}

// handleFileList returns the authenticated account's files for the// attachment management card: expiry ascending, derived ExpiresAt
// (CreatedAt + FileTTL). GET only.
func (s *Server) handleFileList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	who := accountFrom(r.Context())
	files, err := s.store.ListAccountFiles(who)
	if err != nil {
		internalError(w, "list files: "+err.Error())
		return
	}
	if files == nil {
		files = []store.FileSummary{} // stable JSON shape: [] not null
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

// handleFileDownload serves GET /api/files/{id}/download and dispatches
// the management-card mutations: DELETE /api/files/{id} (delete) and
// POST /api/files/{id}/extend (renew expiry to now+TTL).
func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	who := accountFrom(r.Context())
	// POST /api/files/{id}/extend -> ["api","files",id,"extend"]
	if r.Method == http.MethodPost && len(parts) == 4 && parts[3] == "extend" {
		id := parts[2]
		if parts[0] != "api" || parts[1] != "files" || id == "" {
			http.NotFound(w, r)
			return
		}
		// Anti-churn: renewal is harmless but not free — cap per account.
		if !s.allowFileOp(who, extendRateLimit) {
			http.Error(w, fmt.Sprintf("file operation rate limit exceeded (%d/hour)", extendRateLimit), http.StatusTooManyRequests)
			return
		}
		expires, err := s.store.ExtendFile(id, who)
		if err != nil {
			// Foreign, missing, and corrupt ids all look the same.
			http.NotFound(w, r)
			return
		}
		_ = s.audit.Record(r.Context(), audit.ActionFileExtend, who,
			fmt.Sprintf("id=%s expires_at=%d", id, expires))
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "expires_at": expires})
		return
	}
	// DELETE /api/files/{id} -> ["api","files",id]
	if r.Method == http.MethodDelete {
		if len(parts) != 3 || parts[0] != "api" || parts[1] != "files" || parts[2] == "" {
			http.NotFound(w, r)
			return
		}
		id := parts[2]
		if err := s.store.DeleteFile(id, who); err != nil {
			// Foreign, missing, and corrupt ids all look the same.
			http.NotFound(w, r)
			return
		}
		_ = s.audit.Record(r.Context(), audit.ActionFileDelete, who, "id="+id)
		writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	// Route: /api/files/{id}/download -> ["api","files",id,"download"]
	if len(parts) != 4 || parts[0] != "api" || parts[1] != "files" || parts[2] == "" || parts[3] != "download" {
		http.NotFound(w, r)
		return
	}
	id := parts[2]
	code := r.URL.Query().Get("code")
	rec, err := s.store.AuthorizeFileDownload(who, id, code)
	if err != nil {
		// Missing, not-permitted, and bad code all look the same.
		http.NotFound(w, r)
		return
	}
	content, err := s.store.GetFileContent(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", contentDisposition(rec.Filename))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
	_, _ = w.Write(content)
}

// contentDisposition builds a header-safe Content-Disposition value: an
// ASCII-safe fallback filename plus RFC 5987 filename* carrying the
// original (possibly non-ASCII) name. %q alone breaks headers for names
// with non-ASCII bytes (alice's Phase-1 review note).
func contentDisposition(name string) string {
	ascii := make([]rune, 0, len(name))
	for _, r := range name {
		if r >= 0x20 && r < 0x7f && r != '"' && r != '\\' {
			ascii = append(ascii, r)
		}
	}
	fallback := string(ascii)
	if fallback == "" {
		fallback = "file"
	}
	return fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", fallback, url.PathEscape(name))
}

// sanitizeFilename strips path separators and control chars from an
// uploaded name; empty result becomes "file".
func sanitizeFilename(name string) string {
	name = strings.ReplaceAll(strings.ReplaceAll(name, "\\", "/"), "\x00", "")
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "file"
	}
	if len(name) > 255 {
		name = name[len(name)-255:]
	}
	return name
}
