package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Worker-config generation (0.3.7 registration upgrade, boss 12:39:
// batch subordinate registration must ship a ready-to-run worker config
// per minted account — 注册即开箱可用，免手工再配).
//
// Shape follows internal/worker.Config exactly (schema authority: the
// worker package; Efira's reference doc mirrors it). The one-time
// plaintext password exists only inside the registration response — the
// server never stores it, so RE-downloads for existing accounts carry an
// empty "password" plus a fill-in note (template mode).

// workerConfigFor builds a ready-to-run worker.json map for one minted
// subordinate. serverURL is derived from the request Host when present
// (fallback https://<domain>).
func (s *Server) workerConfigFor(r *http.Request, address, password, owner string) map[string]any {
	serverURL := "https://" + s.domain()
	if host := r.Host; host != "" {
		scheme := "https"
		if r.TLS == nil && !strings.HasPrefix(host, "localhost") && !strings.HasPrefix(host, "127.") {
			// behind a plain-HTTP test rig: keep https default; the field is
			// user-editable either way
			_ = scheme
		}
		if host != "" {
			serverURL = scheme + "://" + host
		}
	}
	return map[string]any{
		"server":    serverURL,
		"address":   address,
		"password":  password,
		"cli":       "pi",
		"prompt":    "", // empty = worker applies the v4 default template (placeholders substituted at wake)
		"emergency": map[string]any{"addresses": []string{owner}},
		"full_perm": true,
		"_note":     "save as worker.json next to the worker binary; credentials are one-time — store this file now",
	}
}

// appendWorkerConfig attaches the ready-to-run worker config to one
// registration result entry (in-place on the response map).
func (s *Server) appendWorkerConfig(r *http.Request, entry map[string]any, owner string) {
	if addr, ok := entry["address"].(string); ok && addr != "" {
		pw, _ := entry["password"].(string)
		entry["worker_config"] = s.workerConfigFor(r, addr, pw, owner)
	}
}

// handleSubWorkerConfigWrapper re-roots the path so the shared /api/subs/
// family keeps its routes while config download lives at
// /api/register-subordinate/config/<local>.
func (s *Server) handleSubWorkerConfigWrapper(w http.ResponseWriter, r *http.Request) {
	r.URL.Path = "/api/subs/" + strings.TrimPrefix(r.URL.Path, "/api/register-subordinate/config/")
	s.handleSubWorkerConfig(w, r)
}

// handleSubWorkerConfig serves GET /api/subs/<local>/worker-config for an
// EXISTING subordinate: owner or admin only. The password is never
// recoverable (bcrypt) — the template ships with an empty password and a
// fill-in note.
func (s *Server) handleSubWorkerConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	who := accountFrom(r.Context())
	local := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/subs/"), "/")
	if local == "" || strings.Contains(local, "/") {
		badRequest(w, "subordinate address required")
		return
	}
	target := local
	if !strings.Contains(target, "@") {
		target = local + "@" + s.domain()
	}
	allowed := s.callerIsAdmin(r)
	if !allowed {
		for _, e := range s.store.SubordinatesOf(who) {
			if strings.EqualFold(e.Address, target) {
				allowed = true
				break
			}
		}
	}
	if !allowed {
		http.Error(w, "not your subordinate", http.StatusForbidden)
		return
	}
	if _, err := s.store.GetAccount(target); err != nil {
		http.Error(w, "no such account", http.StatusNotFound)
		return
	}
	cfg := s.workerConfigFor(r, strings.ToLower(target), "", "opencode")
	cfg["password"] = ""
	w.Header().Set("Content-Disposition", "attachment; filename="+local+".worker.json")
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, cfg)
}

var _ = json.Marshal
var _ = fmt.Sprintf
