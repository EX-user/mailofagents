package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/agentmail/agentmail/internal/audit"
	"github.com/agentmail/agentmail/internal/store"
)

// Web Push subscription endpoints (v0.6.30 app notifications,
// docs/push/DESIGN.md). Route A of the approved hybrid: standard Web Push
// carries the system notification; the WebView-fallback polling banner and
// vendor channels live elsewhere. Security rulings enforced here:
//
//   - subscribe/revoke REQUIRE this account's credential (Basic or bearer)
//     and bind the record to that account — no subscribing on someone else's
//     behalf (alice's ownership ruling);
//   - per-account AND per-IP rate limits keep the endpoints from being used
//     as a轰炸 relay (alice's abuse ruling);
//   - the VAPID private key never leaves config; only the public key is
//     served, anonymously, because the SW needs it before login.

// pushSubscribeLimit / pushIPPushLimit are hourly ceilings for subscription
// mutations. Generous for real devices (they re-subscribe rarely), hostile
// to loops.
const (
	pushSubAcctLimit = 30
	pushSubIPLimit   = 60
)

// handleVAPIDKey serves the VAPID public key so a service worker can create
// its PushSubscription before authenticating.
//   GET /api/push/vapid-key -> {"public_key": "..."} | 404 when disabled
func (s *Server) handleVAPIDKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w)
		return
	}
	key := s.cfg.Push.VAPIDPublicKey
	if key == "" {
		http.Error(w, "push not enabled", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"public_key": key})
}

type pushSubRequest struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// handlePushSubscribe registers (or refreshes) the caller's device.
//   POST /api/push/subscribe {endpoint, keys{p256dh, auth}}
func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	who := accountFrom(r.Context())
	ip := clientIP(r)
	s.rateMu.Lock()
	w2 := s.pushRates[who]
	now := time.Now()
	if w2 == nil || now.Sub(w2.windowStart) >= time.Hour {
		w2 = &rateWindow{windowStart: now}
		s.pushRates[who] = w2
	}
	w2.count++
	acctOK := w2.count <= pushSubAcctLimit
	s.rateMu.Unlock()
	if !acctOK || !s.pushIPLimit.allow(ip, pushSubIPLimit, now) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	var req pushSubRequest
	if err := decodeJSON(r, &req); err != nil {
		badRequest(w, "decode body: "+err.Error())
		return
	}
	req.Endpoint = strings.TrimSpace(req.Endpoint)
	if !strings.HasPrefix(req.Endpoint, "https://") || len(req.Endpoint) > 2048 ||
		req.Keys.P256dh == "" || req.Keys.Auth == "" {
		badRequest(w, "invalid subscription")
		return
	}
	rec := &store.PushSubscription{
		Address:   who,
		Endpoint:  req.Endpoint,
		P256dh:    req.Keys.P256dh,
		Auth:      req.Keys.Auth,
		CreatedAt: now.Unix(),
	}
	if err := s.store.UpsertPushSub(rec); err != nil {
		badRequest(w, "store subscription: "+err.Error())
		return
	}
	_ = s.audit.Record(r.Context(), audit.ActionPushSubscribe, who, "device registered")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handlePushRevoke removes one endpoint owned by the caller.
//   DELETE /api/push/subscribe?endpoint=<url>
func (s *Server) handlePushRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		methodNotAllowed(w)
		return
	}
	who := accountFrom(r.Context())
	endpoint := strings.TrimSpace(r.URL.Query().Get("endpoint"))
	if endpoint == "" {
		badRequest(w, "endpoint required")
		return
	}
	if err := s.store.RemovePushSub(who, endpoint); err != nil {
		badRequest(w, "remove subscription: "+err.Error())
		return
	}
	_ = s.audit.Record(r.Context(), audit.ActionPushRevoke, who, "subscription removed")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
