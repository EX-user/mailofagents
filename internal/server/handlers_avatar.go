// Avatar endpoints (0.3.3 feature A, data plane). Contract per the
// team-reviewed draft (Devi → alice, D2 ruling: dedicated avatars bucket):
//
//	PUT    /api/account/avatar   (auth=self) multipart "file" -> {avatar_hash}
//	DELETE /api/account/avatar   (auth=self) -> 200 {}
//	GET    /api/avatar/{address} (auth wall)  -> image bytes, ETag, immutable
//
// Server-side validation is hard-reject (no resizing): magic-number sniff
// jpeg/png only, longest edge ≤ 512px, ≤ 100KB. The client scales before
// upload (canvas, longest edge 512, jpeg/png whichever is smaller) and the
// 413 is the contract for oversized input. Missing avatar on GET is a 404 —
// the client falls back to its address-seeded generated default.
package server

import (
	"bytes"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/agentmail/agentmail/internal/audit"
	"github.com/agentmail/agentmail/internal/store"
)

const avatarMaxEdge = 512

// sniffImage validates magic numbers and returns the decoded dimensions.
// Only jpeg (FFD8FF) and png (89504E47) pass — the browser preview and the
// identicon fallback are the only consumers, so format breadth buys nothing.
func sniffImage(data []byte) (w, h int, ok bool) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, false
	}
	if cfg.Width > avatarMaxEdge || cfg.Height > avatarMaxEdge {
		return 0, 0, false
	}
	// image.DecodeConfig accepts both via registered decoders; enforce the
	// magic numbers explicitly so exotic formats can't slip through.
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return cfg.Width, cfg.Height, true
	}
	if len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}) {
		return cfg.Width, cfg.Height, true
	}
	return 0, 0, false
}

func (s *Server) handleAccountAvatar(w http.ResponseWriter, r *http.Request) {
	who := accountFrom(r.Context())
	switch r.Method {
	case http.MethodPut:
		r.Body = http.MaxBytesReader(w, r.Body, store.AvatarMaxBytes+64<<10)
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			badRequest(w, "invalid multipart form: "+err.Error())
			return
		}
		defer func() { _ = r.MultipartForm.RemoveAll() }()
		f, _, err := r.FormFile("file")
		if err != nil {
			badRequest(w, "file part is required")
			return
		}
		defer f.Close()
		content, err := io.ReadAll(io.LimitReader(f, store.AvatarMaxBytes+1))
		if err != nil {
			badRequest(w, "read file: "+err.Error())
			return
		}
		if len(content) == 0 || len(content) > store.AvatarMaxBytes {
			http.Error(w, "file too large (limit 100KB)", http.StatusRequestEntityTooLarge)
			return
		}
		if _, _, ok := sniffImage(content); !ok {
			http.Error(w, "image must be jpeg/png, longest edge <= 512px", http.StatusRequestEntityTooLarge)
			return
		}
		hash, err := s.store.SaveAvatar(strings.ToLower(who), content)
		if err != nil {
			if errors.Is(err, store.ErrAccountNotFound) {
				http.NotFound(w, r)
				return
			}
			internalError(w, "save avatar: "+err.Error())
			return
		}
		_ = s.audit.Record(r.Context(), audit.ActionAvatarSet, who, "hash="+hash)
		writeJSON(w, http.StatusOK, map[string]any{"avatar_hash": hash})
	case http.MethodDelete:
		if err := s.store.DeleteAvatar(strings.ToLower(who)); err != nil {
			internalError(w, "delete avatar: "+err.Error())
			return
		}
		_ = s.audit.Record(r.Context(), audit.ActionAvatarClear, who, "")
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleAvatarGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "api" || parts[1] != "avatar" || parts[2] == "" {
		http.NotFound(w, r)
		return
	}
	addr := strings.ToLower(parts[2])
	acc, err := s.store.GetAccount(addr)
	if err != nil || acc.AvatarHash == "" {
		http.NotFound(w, r)
		return
	}
	content, err := s.store.GetAvatar(addr)
	if err != nil || len(content) == 0 {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("If-None-Match") == `"`+acc.AvatarHash+`"` {
		w.Header().Set("ETag", `"`+acc.AvatarHash+`"`)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", `"`+acc.AvatarHash+`"`)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("Content-Type", http.DetectContentType(content))
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	_, _ = w.Write(content)
}

// handlePublicAvatar serves GET /api/public/avatar?address=... with NO auth
// wall (D1, boss approved: the guest page shows real in-system avatars).
// Two hard conditions replace the auth check: the account must be
// directory-visible AND have an avatar; anything else is a clean 404 so the
// client falls back to its generated default. Only accounts that opted into
// the directory are exposed — invisibility never leaks bytes.
func (s *Server) handlePublicAvatar(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	addr := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("address")))
	if addr == "" {
		http.NotFound(w, r)
		return
	}
	acc, err := s.store.GetAccount(addr)
	if err != nil || !acc.Visible || acc.AvatarHash == "" {
		http.NotFound(w, r)
		return
	}
	content, err := s.store.GetAvatar(addr)
	if err != nil || len(content) == 0 {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("If-None-Match") == `"`+acc.AvatarHash+`"` {
		w.Header().Set("ETag", `"`+acc.AvatarHash+`"`)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", `"`+acc.AvatarHash+`"`)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Content-Type", http.DetectContentType(content))
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	_, _ = w.Write(content)
}

// exposeAvatarHash adds avatar_hash to a JSON response map when the
// account carries one (omitted otherwise — absence is the "use default"
// signal, matching the store's omitempty encoding).
func exposeAvatarHash(resp map[string]any, acc *store.Account) {
	if acc != nil && acc.AvatarHash != "" {
		resp["avatar_hash"] = acc.AvatarHash
	}
}
