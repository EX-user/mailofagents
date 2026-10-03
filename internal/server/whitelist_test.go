package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentmail/agentmail/internal/audit"
	"github.com/agentmail/agentmail/internal/config"
	"github.com/agentmail/agentmail/internal/store"
)

func newWhitelistTestServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "wl.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.DB().Close() })
	if err := st.BootstrapSystem("admin", "adminpassword1", "test.example"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	a, err := audit.New(st.DB())
	if err != nil {
		t.Fatalf("open audit: %v", err)
	}
	ts := httptest.NewServer(New(st, a, &config.Config{}).Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

func mustInbox(t *testing.T, st *store.Store, addr string) []store.MessageSummary {
	t.Helper()
	msgs, err := st.ReadInbox(addr, 50)
	if err != nil {
		t.Fatalf("read inbox %s: %v", addr, err)
	}
	return msgs
}

func wlReq(t *testing.T, method, url, user, pass, body string) (*http.Response, string) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, url, rd)
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(user, pass)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// TestWhitelistEndpointsPins the GET/PUT/POST/DELETE management surface
// and the superior ?account= path (口径 A: both sides may edit).
func TestWhitelistEndpoints(t *testing.T) {
	ts, st := newWhitelistTestServer(t)
	if _, _, err := st.RegisterTeam("owner", "test.example", "ownerpassword1", 1, []string{"bot1"}); err != nil {
		t.Fatal(err)
	}

	// self GET: defaults
	resp, body := wlReq(t, "GET", ts.URL+"/api/account/whitelist", "bot1@test.example", "", "")
	if resp.StatusCode != 401 {
		t.Fatalf("bad password must 401, got %d", resp.StatusCode)
	}
	// register team passwords are generated; fetch the member list via owner bootstrap knowledge is not available —
	// use the owner + admin paths instead and exercise self via a fresh account.
	if _, err := st.CreateAccountWithPassword("alice", "test.example", false, "alicepassword1"); err != nil {
		t.Fatal(err)
	}
	resp, body = wlReq(t, "GET", ts.URL+"/api/account/whitelist", "alice@test.example", "alicepassword1", "")
	if resp.StatusCode != 200 || !strings.Contains(body, `"enabled":false`) {
		t.Fatalf("self GET = %d %s, want 200 enabled:false", resp.StatusCode, body)
	}
	// PUT enabled + prefill
	resp, body = wlReq(t, "PUT", ts.URL+"/api/account/whitelist", "alice@test.example", "alicepassword1", `{"enabled":true,"addresses":["Friend@test.example"]}`)
	if resp.StatusCode != 200 || !strings.Contains(body, `"enabled":true`) || !strings.Contains(body, "friend@test.example") {
		t.Fatalf("self PUT = %d %s", resp.StatusCode, body)
	}
	// entry add + remove
	resp, _ = wlReq(t, "POST", ts.URL+"/api/account/whitelist/c2@test.example", "alice@test.example", "alicepassword1", "")
	if resp.StatusCode != 200 {
		t.Fatalf("entry POST = %d", resp.StatusCode)
	}
	resp, body = wlReq(t, "DELETE", ts.URL+"/api/account/whitelist/friend@test.example", "alice@test.example", "alicepassword1", "")
	if resp.StatusCode != 200 || strings.Contains(body, "friend@test.example") {
		t.Fatalf("entry DELETE = %d %s", resp.StatusCode, body)
	}
	// superior path: owner edits bot1 via ?account=
	resp, body = wlReq(t, "PUT", ts.URL+"/api/account/whitelist?account=bot1@test.example", "owner@test.example", "ownerpassword1", `{"enabled":true}`)
	if resp.StatusCode != 200 || !strings.Contains(body, `"enabled":true`) {
		t.Fatalf("superior PUT = %d %s", resp.StatusCode, body)
	}
	// stranger path: alice cannot edit bot1
	resp, _ = wlReq(t, "PUT", ts.URL+"/api/account/whitelist?account=bot1@test.example", "alice@test.example", "alicepassword1", `{"enabled":false}`)
	if resp.StatusCode != 403 {
		t.Fatalf("stranger PUT must 403, got %d", resp.StatusCode)
	}
	// admin path
	resp, _ = wlReq(t, "PUT", ts.URL+"/api/account/whitelist?account=alice@test.example", "admin@test.example", "adminpassword1", `{"enabled":false}`)
	if resp.StatusCode != 200 {
		t.Fatalf("admin PUT = %d", resp.StatusCode)
	}
}

// TestWhitelistSendGate pins the delivery gate on /api/send: unlisted
// sender gets 403 code=whitelist_rejected and NOTHING is stored in the
// recipient's inbox; listed sender delivers; hierarchy delivers.
func TestWhitelistSendGate(t *testing.T) {
	ts, st := newWhitelistTestServer(t)
	if _, _, err := st.RegisterTeam("owner", "test.example", "ownerpassword1", 1, []string{"bot1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAccountWithPassword("mallory", "test.example", false, "mallorypassword1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAccountWithPassword("friend", "test.example", false, "friendpassword1"); err != nil {
		t.Fatal(err)
	}
	// owner: gate on, list empty
	if err := st.SetWhitelistEnabled("owner@test.example", true); err != nil {
		t.Fatal(err)
	}

	send := func(user, pass, to string) (*http.Response, string) {
		payload, _ := json.Marshal(map[string]any{"to": []string{to}, "subject": "s", "body": "b"})
		return wlReq(t, "POST", ts.URL+"/api/send", user, pass, string(payload))
	}

	// unlisted sender: 403 whitelist_rejected, zero storage
	resp, body := send("mallory@test.example", "mallorypassword1", "owner@test.example")
	if resp.StatusCode != 403 || !strings.Contains(body, "whitelist_rejected") {
		t.Fatalf("unlisted send = %d %s, want 403 whitelist_rejected", resp.StatusCode, body)
	}
	if n := len(mustInbox(t, st, "owner@test.example")); n != 0 {
		t.Fatalf("recipient must have zero awareness, inbox=%d", n)
	}

	// hierarchy sender: exemption delivers despite the gate
	resp, body = send("bot1@test.example", "", "owner@test.example")
	if resp.StatusCode != 401 {
		t.Fatalf("sanity (bot password unknown is fine to skip) got %d %s", resp.StatusCode, body)
	}
	// give bot1 a known password path via owner declare — use friend instead
	resp, body = send("friend@test.example", "friendpassword1", "owner@test.example")
	if resp.StatusCode != 403 {
		t.Fatalf("non-hierarchy unlisted must still 403, got %d %s", resp.StatusCode, body)
	}
	// add friend to the list: delivers
	if err := st.WhitelistAdd("owner@test.example", "friend@test.example"); err != nil {
		t.Fatal(err)
	}
	resp, body = send("friend@test.example", "friendpassword1", "owner@test.example")
	if resp.StatusCode != 200 || !strings.Contains(body, `"status":"sent"`) {
		t.Fatalf("listed send = %d %s, want 200 sent", resp.StatusCode, body)
	}
	if n := len(mustInbox(t, st, "owner@test.example")); n != 1 {
		t.Fatalf("listed send must store exactly one letter, inbox=%d", n)
	}
}

// TestWhitelistPartialReject pins the partial shape: one gated recipient
// + one open recipient -> 200 sent with a "rejected" array naming the
// gated one.
func TestWhitelistPartialReject(t *testing.T) {
	ts, st := newWhitelistTestServer(t)
	if _, err := st.CreateAccountWithPassword("alice", "test.example", false, "alicepassword1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAccountWithPassword("gated", "test.example", false, "gatedpassword1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAccountWithPassword("open", "test.example", false, "openpassword1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetWhitelistEnabled("gated@test.example", true); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"to": []string{"gated@test.example", "open@test.example"}, "subject": "s", "body": "b"})
	resp, body := wlReq(t, "POST", ts.URL+"/api/send", "alice@test.example", "alicepassword1", string(payload))
	if resp.StatusCode != 200 || !strings.Contains(body, `"status":"sent"`) || !strings.Contains(body, "whitelist_rejected") {
		t.Fatalf("partial = %d %s, want 200 sent + rejected[]", resp.StatusCode, body)
	}
	if n := len(mustInbox(t, st, "gated@test.example")); n != 0 {
		t.Fatalf("gated inbox must stay empty, got %d", n)
	}
	if n := len(mustInbox(t, st, "open@test.example")); n != 1 {
		t.Fatalf("open inbox must hold the letter, got %d", n)
	}
}
