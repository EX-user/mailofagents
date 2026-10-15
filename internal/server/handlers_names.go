package server

import (
	"net/http"
	"strings"
)

// handleNamesAvailable answers "which of these names are free?" for the
// portal's pre-registration scan (boss directive 10-12: check before you
// register, don't partial-fail).
//
//	GET /api/names/available?names=alpha,beta,gamma
//	-> {"available": {"alpha": true, "beta": false, ...}, "invalid": ["!!bad"]}
//
// Public (matches registration's public posture — name existence is
// already exposed by the register flow's 409); invalid shapes are
// reported separately rather than erroring the whole call; max 20 names
// per call (probe-cap). Local-parts only (domain implied).
func (s *Server) handleNamesAvailable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	raw := strings.TrimSpace(r.URL.Query().Get("names"))
	if raw == "" {
		badRequest(w, "names query required")
		return
	}
	fields := strings.FieldsFunc(raw, func(rn rune) bool {
		return strings.ContainsRune(",;+ \t、，；", rn)
	})
	seen := map[string]bool{}
	names := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.ToLower(strings.TrimSpace(f))
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		names = append(names, f)
	}
	if len(names) == 0 {
		badRequest(w, "no usable names")
		return
	}
	if len(names) > 20 {
		badRequest(w, "max 20 names per call")
		return
	}
	domain := s.domain()
	available := make(map[string]bool, len(names))
	invalid := []string{}
	for _, n := range names {
		if len(n) > 32 || !isASCIILocalPart(n) {
			invalid = append(invalid, n)
			continue
		}
		_, err := s.store.GetAccount(n+"@"+domain)
		available[n] = err != nil // free when no account exists
	}
	if invalid == nil {
		invalid = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": available, "invalid": invalid})
}
