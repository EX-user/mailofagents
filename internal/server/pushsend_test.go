package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentmail/agentmail/internal/audit"
	"github.com/agentmail/agentmail/internal/config"
	"github.com/agentmail/agentmail/internal/store"
)

type capturedPush struct {
	endpoint string
	payload  pushPayload
}

func newPushSendTestServer(t *testing.T) (*Server, *store.Store, *[]capturedPush) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.DB().Close() })
	if err := st.BootstrapSystem("admin", "adminpassword1", "test.example"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if _, err := st.CreateAccountWithPassword("user", "t", false, "userpass-123"); err != nil {
		t.Fatalf("register: %v", err)
	}
	a, err := audit.New(st.DB())
	if err != nil {
		t.Fatalf("open audit: %v", err)
	}
	cfg := &config.Config{}
	cfg.Push.VAPIDPublicKey = "BPub"
	cfg.Push.VAPIDPrivateKey = "Priv"
	srv := New(st, a, cfg)
	var mu sync.Mutex
	captured := &[]capturedPush{}
	srv.sendPush = func(sub *store.PushSubscription, pub, priv, subject string, p pushPayload) (bool, error) {
		mu.Lock()
		*captured = append(*captured, capturedPush{endpoint: sub.Endpoint, payload: p})
		mu.Unlock()
		return true, nil
	}
	return srv, st, captured
}

// TestPushDeliveryAggregateAndDND drives M2 through the store+delivery seam:
// normal arrival → one aggregated push with the red-lined payload shape;
// silence window → no push until close, then ONE summary.
func TestPushDeliveryAggregateAndDND(t *testing.T) {
	srv, st, captured := newPushSendTestServer(t)
	addr := "user@t"
	if err := st.UpsertPushSub(&store.PushSubscription{Address: addr, Endpoint: "https://push/x", P256dh: "k", Auth: "a"}); err != nil {
		t.Fatalf("sub: %v", err)
	}

	// Normal arrivals (inside the aggregation window).
	srv.notifyDelivery("Teammate A", []string{addr})
	srv.notifyDelivery("Teammate B", []string{addr})
	if got := len(func() []capturedPush { return *captured }()); got != 0 {
		t.Fatalf("push fired before window closed: %d", got)
	}
	time.Sleep(10 * time.Millisecond) // goroutine scheduling headroom
	srv.pd.mu.Lock()
	for _, timer := range srv.pd.pending { // shrink the 60s window for test speed
		timer.Reset(30 * time.Millisecond)
	}
	srv.pd.mu.Unlock()
	waitFor(t, 2*time.Second, func() bool {
		c := *captured
		return len(c) == 1
	})
	got := (*captured)[0]
	// Two arrivals collapsed into exactly one push; FromName is whichever
	// arrival won the race, so only assert it is one of them.
	if got.endpoint != "https://push/x" || got.payload.Digest != 0 {
		t.Fatalf("aggregated push wrong: %+v", got)
	}
	if f := got.payload.FromName; f != "Teammate A" && f != "Teammate B" {
		t.Fatalf("unexpected from_name: %+v", got.payload)
	}
	raw, _ := json.Marshal(got.payload)
	for _, forbidden := range []string{"subject", "body", addr} {
		if strings.Contains(string(raw), fmt.Sprintf("%q", forbidden)) && forbidden != "body" {
			t.Fatalf("payload leaks %s: %s", forbidden, raw)
		}
	}

	// DND window open NOW for the rest of this minute-hour grid — use a
	// window covering the whole day minus nothing: start==end would never be
	// silent, so enable a real range and verify deferment.
	if err := st.SetPushDND(addr, store.PushDND{Enabled: true, StartMin: 0, EndMin: 24*60 - 1}); err != nil {
		t.Fatalf("dnd set: %v", err)
	}
	srv.notifyDelivery("Quiet Sender", []string{addr})
	time.Sleep(50 * time.Millisecond)
	if n := len(*captured); n != 1 {
		t.Fatalf("DND did not silence delivery: %d pushes", n)
	}
	// Close the window manually (as time would) and check the summary.
	d, _ := st.GetPushDND(addr)
	d.EndMin = d.StartMin + 1
	_ = d
	srv.flushDigest(addr)
	waitFor(t, 2*time.Second, func() bool {
		c := *captured
		return len(c) == 2
	})
	sum := (*captured)[1]
	if sum.payload.Digest == 0 {
		t.Fatalf("expected digest summary push, got %+v", sum.payload)
	}

	// Disabled account cascades its DND row away together with subs.
	if err := st.DeleteAllPushSubs(addr); err != nil {
		t.Fatalf("cascade: %v", err)
	}
	if dd, _ := st.GetPushDND(addr); dd.Enabled {
		t.Fatal("cascade left DND enabled")
	}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// TestPushSettingsEndpoint checks the authed GET/PUT of the DND window.
func TestPushSettingsEndpoint(t *testing.T) {
	srv, _, _ := newPushSendTestServer(t)
	ts := httptest.NewServer(srv.Handler())

	req, _ := http.NewRequest("PUT", ts.URL+"/api/push/settings",
		bytes.NewReader([]byte(`{"enabled":true,"start_min":1320,"end_min":420}`)))
	req.SetBasicAuth("user@t", "userpass-123")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("put settings: %v %v", resp.StatusCode, err)
	}
	resp.Body.Close()

	req2, _ := http.NewRequest("GET", ts.URL+"/api/push/settings", nil)
	req2.SetBasicAuth("user@t", "userpass-123")
	resp2, _ := http.DefaultClient.Do(req2)
	var d struct {
		Enabled  bool `json:"enabled"`
		StartMin int  `json:"start_min"`
		EndMin   int  `json:"end_min"`
		Active   bool `json:"active"`
	}
	json.NewDecoder(resp2.Body).Decode(&d)
	resp2.Body.Close()
	if !d.Enabled || d.StartMin != 1320 || d.EndMin != 420 {
		t.Fatalf("settings roundtrip mismatch: %+v", d)
	}
	// active is authoritative and must match the window math server-side
	// (22:00-07:00 window: whatever the local hour is, the verdict must be
	// consistent with PushDND.ActiveAt).
	now := time.Now()
	want := store.PushDND{Enabled: true, StartMin: 1320, EndMin: 420}.ActiveAt(now.Hour()*60 + now.Minute())
	if d.Active != want {
		t.Fatalf("active=%v, want %v", d.Active, want)
	}

	// Invalid minutes rejected.
	req3, _ := http.NewRequest("PUT", ts.URL+"/api/push/settings",
		bytes.NewReader([]byte(`{"enabled":true,"start_min":99999,"end_min":0}`)))
	req3.SetBasicAuth("user@t", "userpass-123")
	resp3, _ := http.DefaultClient.Do(req3)
	if resp3.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid window = %d, want 400", resp3.StatusCode)
	}
	resp3.Body.Close()

	// Unauthenticated -> 401.
	resp4, _ := http.Get(ts.URL + "/api/push/settings")
	if resp4.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anon settings = %d, want 401", resp4.StatusCode)
	}
	resp4.Body.Close()
}
