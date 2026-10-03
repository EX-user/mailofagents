package server

import (
	"net/http"
	"strings"
)

// Account whitelist endpoints (0.3.7, boss directive 10-03 23:51).
//
//	GET    /api/account/whitelist              -> {"account","enabled","addresses"}
//	PUT    /api/account/whitelist              {"enabled":bool,"addresses":[...]} (both optional; ?account= for superior/admin)
//	POST   /api/account/whitelist/<address>    add one entry (self)
//	DELETE /api/account/whitelist/<address>    remove one entry (self)
//
// Access (spec point 5, 口径 A — no locking, both sides may edit): the
// account itself, its superior, or the admin. GET/PUT accept
// ?account=<address> for the superior/admin path (toggle + full set);
// entry routes are self-service (the superior sets the whole list via
// PUT).
func (s *Server) handleAccountWhitelist(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		methodNotAllowed(w)
		return
	}
	who := accountFrom(r.Context())
	target := who
	if q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("account"))); q != "" && q != who {
		if !s.canEditWhitelist(r, who, q) {
			http.Error(w, "not permitted to edit this account's whitelist", http.StatusForbidden)
			return
		}
		target = q
	}
	acc, err := s.store.GetAccount(target)
	if err != nil {
		http.Error(w, "account not found", http.StatusNotFound)
		return
	}
	if r.Method == http.MethodPut {
		var body struct {
			Enabled   *bool    `json:"enabled"`
			Addresses []string `json:"addresses"`
		}
		if err := decodeJSON(r, &body); err != nil {
			badRequest(w, "invalid body: "+err.Error())
			return
		}
		if body.Addresses != nil {
			if err := s.store.WhitelistSet(target, body.Addresses); err != nil {
				http.Error(w, "set whitelist: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if body.Enabled != nil {
			if err := s.store.SetWhitelistEnabled(target, *body.Enabled); err != nil {
				http.Error(w, "set enabled: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		acc, _ = s.store.GetAccount(target)
	}
	addrs := acc.Whitelist
	if addrs == nil {
		addrs = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": target, "enabled": acc.WhitelistEnabled, "addresses": addrs})
}

func (s *Server) handleAccountWhitelistEntry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		methodNotAllowed(w)
		return
	}
	who := accountFrom(r.Context())
	entry := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/api/account/whitelist/")))
	if entry == "" {
		badRequest(w, "address required")
		return
	}
	local := strings.SplitN(entry, "@", 2)[0]
	if !isASCIILocalPart(local) {
		badRequest(w, "invalid address")
		return
	}
	switch r.Method {
	case http.MethodPost:
		if err := s.store.WhitelistAdd(who, entry); err != nil {
			http.Error(w, "add: "+err.Error(), http.StatusInternalServerError)
			return
		}
	case http.MethodDelete:
		if err := s.store.WhitelistRemove(who, entry); err != nil {
			http.Error(w, "remove: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	acc, _ := s.store.GetAccount(who)
	addrs := acc.Whitelist
	if addrs == nil {
		addrs = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": who, "enabled": acc.WhitelistEnabled, "addresses": addrs})
}

// canEditWhitelist answers "may who edit target's whitelist?": admin
// always; the target's superior per the declare edges.
func (s *Server) canEditWhitelist(r *http.Request, who, target string) bool {
	if s.callerIsAdmin(r) {
		return true
	}
	for _, e := range s.store.SubordinatesOf(who) {
		if strings.EqualFold(e.Address, target) {
			return true
		}
	}
	return false
}
