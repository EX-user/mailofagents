package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/agentmail/agentmail/internal/audit"
	"github.com/agentmail/agentmail/internal/store"
)

// Push delivery (v0.6.30 M2): a successful LOCAL delivery fans out web
// pushes to every subscription of the recipient. Three rulings shape this
// file:
//
//   - payload red line: {"unread_count", "from_name"} ONLY — no body, no
//     subject, no recipient address (the push middleman is untrusted);
//   - 60s per-account aggregation window: N arrivals collapse into one push,
//     so a burst cannot become a notification storm;
//   - DND is decided SERVER-side against the account's stored window
//     (client clocks untrusted). Arrivals during silence are counted; ONE
//     summary push ("N arrived during quiet hours") fires when it closes.

type pushPayload struct {
	UnreadCount int    `json:"unread_count"`
	FromName    string `json:"from_name"`
	Digest      int    `json:"digest,omitempty"` // >0 = quiet-hours summary: N letters arrived while silent
}

type pushDelivery struct {
	mu           sync.Mutex
	pending      map[string]*time.Timer // account -> aggregation flush timer
	latestFrom   map[string]string      // account -> most recent sender's display name
	digestCount  map[string]int         // account -> arrivals while silent
	digestTimers map[string]*time.Timer // account -> window-close summary timer
}

func newPushDelivery() *pushDelivery {
	return &pushDelivery{
		pending:      map[string]*time.Timer{},
		latestFrom:   map[string]string{},
		digestCount:  map[string]int{},
		digestTimers: map[string]*time.Timer{},
	}
}

// notifyDelivery is the hook handleSend calls after a successful local
// delivery. It never fails (nor slows) the send: each recipient is handled
// in its own goroutine.
func (s *Server) notifyDelivery(fromName string, recipients []string) {
	if s.cfg.Push.VAPIDPublicKey == "" {
		return // push disabled: zero overhead on sends
	}
	for _, rcpt := range recipients {
		go s.notifyRecipient(rcpt, fromName)
	}
}

func (s *Server) notifyRecipient(addr, fromName string) {
	now := time.Now()
	dnd, err := s.store.GetPushDND(addr)
	if err != nil || !dnd.ActiveAt(now.Hour()*60+now.Minute()) {
		s.aggregateOrPush(addr, fromName)
		return
	}
	s.enqueueDigest(addr)
}

// pushAggWindow collapses arrivals into one push per account.
const pushAggWindow = 60 * time.Second

func (s *Server) aggregateOrPush(addr, fromName string) {
	s.pd.mu.Lock()
	if _, ok := s.pd.pending[addr]; !ok {
		s.pd.pending[addr] = time.AfterFunc(pushAggWindow, func() { s.flushAggregated(addr) })
	}
	s.pd.latestFrom[addr] = fromName
	s.pd.mu.Unlock()
}

func (s *Server) flushAggregated(addr string) {
	s.pd.mu.Lock()
	delete(s.pd.pending, addr)
	from := s.pd.latestFrom[addr]
	delete(s.pd.latestFrom, addr)
	s.pd.mu.Unlock()

	unread, _ := s.store.CountUnread(addr)
	s.sendToSubs(addr, pushPayload{UnreadCount: unread, FromName: from})
}

// enqueueDigest records an arrival inside an active DND window and arms ONE
// summary at window close (the timer captures the then-current count at fire
// time, so later arrivals still land in the same summary).
func (s *Server) enqueueDigest(addr string) {
	s.pd.mu.Lock()
	s.pd.digestCount[addr]++
	var wait time.Duration
	dnd, _ := s.store.GetPushDND(addr)
	now := time.Now()
	minOfDay := now.Hour()*60 + now.Minute()
	switch {
	case dnd.StartMin < dnd.EndMin:
		wait = time.Duration(dnd.EndMin-minOfDay) * time.Minute
	default: // window wraps midnight
		wait = time.Duration(24*60+dnd.EndMin-minOfDay) * time.Minute
	}
	if t, ok := s.pd.digestTimers[addr]; ok && t != nil {
		t.Reset(wait) // keep pushing the deadline as mail keeps arriving
	} else {
		s.pd.digestTimers[addr] = time.AfterFunc(wait, func() { s.flushDigest(addr) })
	}
	s.pd.mu.Unlock()
}

func (s *Server) flushDigest(addr string) {
	s.pd.mu.Lock()
	n := s.pd.digestCount[addr]
	delete(s.pd.digestCount, addr)
	delete(s.pd.digestTimers, addr)
	s.pd.mu.Unlock()
	if n <= 0 {
		return
	}
	unread, _ := s.store.CountUnread(addr)
	s.sendToSubs(addr, pushPayload{UnreadCount: unread, Digest: n})
}

// sendPushFunc is the test seam: return alive=false with err!=nil to signal
// a dead endpoint (pruned).
type sendPushFunc func(sub *store.PushSubscription, pubKey, privKey, subject string, p pushPayload) (alive bool, err error)

// sendToSubs pushes the payload to every live subscription of the account,
// pruning endpoints the service reports as gone.
func (s *Server) sendToSubs(addr string, p pushPayload) {
	pub, priv := s.cfg.Push.VAPIDPublicKey, s.cfg.Push.VAPIDPrivateKey
	subject := s.vapidSubject
	if subject == "" {
		subject = "mailto:admin@" + s.domain()
	}
	data, _ := json.Marshal(p)
	subs, err := s.store.PushSubsByAddress(addr)
	if err != nil {
		return
	}
	for _, sub := range subs {
		if s.sendPush != nil { // tests only; implies push configured
			if alive, err := s.sendPush(sub, pub, priv, subject, p); err != nil && !alive {
				_ = s.store.RemovePushSub(addr, sub.Endpoint)
			}
			continue
		}
		if pub == "" || priv == "" {
			return
		}
		resp, err := webpush.SendNotification(data, &webpush.Subscription{
			Endpoint: sub.Endpoint,
			Keys:     webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth},
		}, &webpush.Options{
			VAPIDPrivateKey: priv,
			VAPIDPublicKey:  pub,
			Subscriber:      subject,
			TTL:             3600,
		})
		if err != nil {
			fmt.Printf("push send failed (%s): %v\n", maskEndpoint(sub.Endpoint), err)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusNotFound {
			_ = s.store.RemovePushSub(addr, sub.Endpoint)
		}
	}
}

func maskEndpoint(ep string) string {
	if len(ep) > 40 {
		return ep[:40] + "..."
	}
	return ep
}

// --- DND settings API ---

// handlePushSettings reads or updates the caller's do-not-disturb window.
//   GET /api/push/settings             -> current window
//   PUT /api/push/settings {...window} -> persisted
func (s *Server) handlePushSettings(w http.ResponseWriter, r *http.Request) {
	who := accountFrom(r.Context())
	switch r.Method {
	case http.MethodGet:
		d, err := s.store.GetPushDND(who)
		if err != nil {
			internalError(w, "read dnd: "+err.Error())
			return
		}
		// active is the server's authoritative "silenced right now" verdict —
		// the UI's status chip reads this instead of recomputing clocks.
		now := time.Now()
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled":   d.Enabled,
			"start_min": d.StartMin,
			"end_min":   d.EndMin,
			"active":    d.ActiveAt(now.Hour()*60 + now.Minute()),
		})
	case http.MethodPut:
		var d store.PushDND
		if err := decodeJSON(r, &d); err != nil {
			badRequest(w, "decode body: "+err.Error())
			return
		}
		if err := s.store.SetPushDND(who, d); err != nil {
			badRequest(w, "store dnd: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		methodNotAllowed(w)
	}
}

// --- display local-part (V06_DISPLAY_ADDRESS) ---

// handleDisplayLocal reads the caller's case-preserved local part. Sole
// display surface: the settings page / self-query — mail faces stay
// all-lowercase (superior ruling 01M13ZXK). READ-ONLY per superior ruling
// (01M14HSA): the value comes only from registration input; no edit entry.
//   GET /api/account/display-local             -> {"display_local": "..."} ("" = unset)
func (s *Server) handleDisplayLocal(w http.ResponseWriter, r *http.Request) {
	who := accountFrom(r.Context())
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	d, err := s.store.GetDisplayLocal(who)
	if err != nil {
		internalError(w, "read display: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"display_local": d})
}

// --- site copy (v0.1.2, admin-configurable brand surfaces) ---

// handleSiteCopyGet serves the configured copy (empty fields = built-in
// defaults in use). Public: the guest portal renders before login.
//   GET /api/site-copy
func (s *Server) handleSiteCopyGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w)
		return
	}
	sc, err := s.store.GetSiteCopy()
	if err != nil {
		internalError(w, "read site copy: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sc.PublicMap())
}

// handleSiteCopySet updates brand copy (admin only, partial update).
//   PUT /admin/site-copy {portal_tagline_zh?: "...", ...}
// Semantics (v0.1.5): an ABSENT key leaves its value untouched; a key set
// to "" CLEARS the override so the built-in default shows through again
// (the panel submits all six keys on every save). Unknown keys are rejected.
func (s *Server) handleSiteCopySet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		methodNotAllowed(w)
		return
	}
	var raw map[string]json.RawMessage
	if err := decodeJSON(r, &raw); err != nil {
		badRequest(w, "decode body: "+err.Error())
		return
	}
	metaByKey := map[string]string{
		"portal_tagline_zh": "site_portal_tagline_zh",
		"portal_tagline_en": "site_portal_tagline_en",
		"portal_title_zh":   "site_portal_title_zh",
		"portal_title_en":   "site_portal_title_en",
		"panel_title_zh":    "site_panel_title_zh",
		"panel_title_en":    "site_panel_title_en",
	}
	set := make(map[string]string, len(raw))
	var clear []string
	for k, rv := range raw {
		meta, ok := metaByKey[k]
		if !ok {
			badRequest(w, "unknown key: "+k)
			return
		}
		var val string
		if err := json.Unmarshal(rv, &val); err != nil {
			badRequest(w, k+": value must be a string")
			return
		}
		if val == "" {
			clear = append(clear, meta)
			continue
		}
		set[meta] = val
	}
	if err := s.store.UpdateSiteCopy(set, clear); err != nil {
		badRequest(w, "site copy: "+err.Error())
		return
	}
	who := accountFrom(r.Context())
	_ = s.audit.Record(r.Context(), audit.ActionSetSiteCopy, who, "brand copy updated")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
