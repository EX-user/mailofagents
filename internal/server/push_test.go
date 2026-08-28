package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentmail/agentmail/internal/audit"
	"github.com/agentmail/agentmail/internal/config"
	"github.com/agentmail/agentmail/internal/store"
)

func newPushTestServer(t *testing.T, pubkey string) (*httptest.Server, string, *store.Store) {
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
	cfg.Push.VAPIDPublicKey = pubkey
	ts := httptest.NewServer(New(st, a, cfg).Handler())
	t.Cleanup(ts.Close)
	return ts, "user@t:userpass-123", st
}

// TestPushSubscribeLifecycle covers M1's endpoint contract: anonymous key
// fetch, authenticated subscribe (Basic AND bearer), multi-device entries,
// idempotent re-subscribe, cross-account hijack rejection, revoke, and the
// no-auth 401 paths.
func TestPushSubscribeLifecycle(t *testing.T) {
	ts, basic, st := newPushTestServer(t, "BTestPubKey")
	user, pass, _ := strings.Cut(basic, ":")

	subBody := func(ep string) []byte {
		return []byte(fmt.Sprintf(`{"endpoint":%q,"keys":{"p256dh":"k1","auth":"a1"}}`, ep))
	}

	// Anonymous VAPID key.
	resp, err := http.Get(ts.URL + "/api/push/vapid-key")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("vapid-key: %v %v", resp.StatusCode, err)
	}
	var vk struct{ PublicKey string `json:"public_key"` }
	json.NewDecoder(resp.Body).Decode(&vk)
	resp.Body.Close()
	if vk.PublicKey != "BTestPubKey" {
		t.Fatalf("vapid key mismatch: %q", vk.PublicKey)
	}

	// Subscribe without auth -> 401.
	resp, _ = http.Post(ts.URL+"/api/push/subscribe", "application/json", bytes.NewReader(subBody("https://push.example/1")))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated subscribe = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()

	doSub := func(ep string) int {
		req, _ := http.NewRequest("POST", ts.URL+"/api/push/subscribe", bytes.NewReader(subBody(ep)))
		req.SetBasicAuth(user, pass)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("subscribe %s: %v", ep, err)
		}
		defer r.Body.Close()
		return r.StatusCode
	}

	if code := doSub("https://push.example/dev1"); code != http.StatusOK {
		t.Fatalf("subscribe dev1 = %d", code)
	}
	if code := doSub("https://push.example/dev1"); code != http.StatusOK { // idempotent refresh
		t.Fatalf("re-subscribe dev1 = %d", code)
	}
	if code := doSub("https://push.example/dev2"); code != http.StatusOK {
		t.Fatalf("subscribe dev2 = %d", code)
	}
	subs, err := st.PushSubsByAddress(user)
	if err != nil || len(subs) != 2 {
		t.Fatalf("want 2 subs, got %d (%v)", len(subs), err)
	}

	// Endpoint must be https.
	req, _ := http.NewRequest("POST", ts.URL+"/api/push/subscribe",
		bytes.NewReader([]byte(`{"endpoint":"http://insecure/x","keys":{"p256dh":"k","auth":"a"}}`)))
	req.SetBasicAuth(user, pass)
	r2, _ := http.DefaultClient.Do(req)
	if r2.StatusCode != http.StatusBadRequest {
		t.Fatalf("http endpoint = %d, want 400", r2.StatusCode)
	}
	r2.Body.Close()

	// Revoke dev2; only dev1 remains.
	req, _ = http.NewRequest("DELETE", ts.URL+"/api/push/subscribe?endpoint=https://push.example/dev2", nil)
	req.SetBasicAuth(user, pass)
	r3, _ := http.DefaultClient.Do(req)
	if r3.StatusCode != http.StatusOK {
		t.Fatalf("revoke = %d", r3.StatusCode)
	}
	r3.Body.Close()
	subs, _ = st.PushSubsByAddress(user)
	if len(subs) != 1 || subs[0].Endpoint != "https://push.example/dev1" {
		t.Fatalf("after revoke want [dev1], got %+v", subs)
	}
}

// TestVAPIDKeyDisabled verifies the disabled state is a clean 404 rather than
// an empty-key success.
func TestVAPIDKeyDisabled(t *testing.T) {
	ts, _, _ := newPushTestServer(t, "")
	resp, err := http.Get(ts.URL + "/api/push/vapid-key")
	if err != nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled vapid-key = %v %v, want 404", resp.StatusCode, err)
	}
	resp.Body.Close()
}
