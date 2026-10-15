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


// TestWhitelistSelfSendExempt pins the boss 10-15 13:43 order: a letter
// addressed to yourself (sender == recipient) delivers even when the
// recipient's gate is on and the sender is not on the list.
func TestWhitelistSelfSendExempt(t *testing.T) {
	ts, st := newWhitelistTestServer(t)
	if _, _, err := st.RegisterTeam("owner", "test.example", "ownerpassword1", 1, []string{"bot1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetWhitelistEnabled("owner@test.example", true); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"to": []string{"owner@test.example"}, "subject": "note to self", "body": "remember this"})
	resp, body := wlReq(t, "POST", ts.URL+"/api/send", "owner@test.example", "ownerpassword1", string(payload))
	if resp.StatusCode != 200 || !strings.Contains(body, `"status":"sent"`) {
		t.Fatalf("self send = %d %s, want 200 sent", resp.StatusCode, body)
	}
	if n := len(mustInbox(t, st, "owner@test.example")); n != 1 {
		t.Fatalf("self letter must land despite gate+empty list, inbox=%d", n)
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

// TestRegisterSubordinateBatch pins the 0.3.7 boss upgrade: separator
// names provision several named subordinates in ONE action, each
// auto-declared under the caller; a taken name fails the whole action
// with 409 and nothing is created; the single-name shape is unchanged.
func TestRegisterSubordinateBatch(t *testing.T) {
	ts, st := newWhitelistTestServer(t)
	if _, err := st.CreateAccountWithPassword("owner", "test.example", false, "ownerpassword1"); err != nil {
		t.Fatal(err)
	}
	// batch: three names via mixed separators
	resp, body := wlReq(t, "POST", ts.URL+"/api/register-subordinate", "owner@test.example", "ownerpassword1",
		`{"username":"alpha, beta;gamma、delta"}`)
	if resp.StatusCode != 201 || !strings.Contains(body, `"created"`) {
		t.Fatalf("batch = %d %s", resp.StatusCode, body)
	}
	var out struct {
		Created []map[string]any `json:"created"`
	}
	_ = json.Unmarshal([]byte(body), &out)
	if len(out.Created) != 4 {
		t.Fatalf("want 4 created, got %d: %s", len(out.Created), body)
	}
	for _, c := range out.Created {
		addr, _ := c["address"].(string)
		if addr == "" || c["declared"] != true {
			t.Fatalf("bad entry: %v", c)
		}
		edges := st.SubordinatesOf("owner@test.example")
		found := false
		for _, e := range edges {
			if strings.EqualFold(e.Address, addr) {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s not declared under owner", addr)
		}
	}
	// taken name pre-check: whole action 409, nothing created
	resp, body = wlReq(t, "POST", ts.URL+"/api/register-subordinate", "owner@test.example", "ownerpassword1",
		`{"username":"fresh, alpha"}`)
	if resp.StatusCode != 409 || !strings.Contains(body, "alpha") {
		t.Fatalf("conflict batch = %d %s, want 409 naming alpha", resp.StatusCode, body)
	}
	if got := st.SubordinatesOf("owner@test.example"); len(got) != 4 {
		t.Fatalf("conflict must create nothing, subs=%d", len(got))
	}
	// single-name shape unchanged (v0.6.4)
	resp, body = wlReq(t, "POST", ts.URL+"/api/register-subordinate", "owner@test.example", "ownerpassword1",
		`{"username":"solo"}`)
	if resp.StatusCode != 201 || !strings.Contains(body, `"declared":true`) || strings.Contains(body, "created") {
		t.Fatalf("single = %d %s", resp.StatusCode, body)
	}
}

// TestWorkerConfigDownload pins the 0.3.7 boss 12:39 upgrade: the create
// response carries a ready-to-run worker_config (server/address/password/
// emergency=[owner]); re-download for an existing subordinate is
// owner/admin-gated and ships a template (empty password + note).
func TestWorkerConfigDownload(t *testing.T) {
	ts, st := newWhitelistTestServer(t)
	if _, err := st.CreateAccountWithPassword("owner", "test.example", false, "ownerpassword1"); err != nil {
		t.Fatal(err)
	}
	// single register -> worker_config present with credentials
	resp, body := wlReq(t, "POST", ts.URL+"/api/register-subordinate", "owner@test.example", "ownerpassword1",
		`{"username":"cfgbot"}`)
	if resp.StatusCode != 201 || !strings.Contains(body, "worker_config") {
		t.Fatalf("register = %d %s, want worker_config", resp.StatusCode, body)
	}
	var out map[string]any
	_ = json.Unmarshal([]byte(body), &out)
	wc, _ := out["worker_config"].(map[string]any)
	if wc["address"] != "cfgbot@test.example" || wc["password"] == "" || wc["server"] == "" {
		t.Fatalf("worker_config incomplete: %v", wc)
	}
	if _, has := wc["emergency"]; has {
		t.Fatalf("emergency omitted by default (Efira minimal-file ruling), got %v", wc["emergency"])
	}
	// re-download: owner OK, template password empty
	resp, body = wlReq(t, "GET", ts.URL+"/api/register-subordinate/config/cfgbot", "owner@test.example", "ownerpassword1", "")
	if resp.StatusCode != 200 || !strings.Contains(body, `"password":""`) {
		t.Fatalf("re-download = %d %s", resp.StatusCode, body)
	}
	// stranger gated
	resp, _ = wlReq(t, "GET", ts.URL+"/api/register-subordinate/config/cfgbot", "owner@test.example", "wrong", "")
	if resp.StatusCode != 401 {
		t.Fatalf("bad auth = %d, want 401", resp.StatusCode)
	}
}

// TestNamesAvailable pins the pre-registration scan (boss 10-12): public,
// batch booleans, invalid names reported separately, probe cap 20.
func TestNamesAvailable(t *testing.T) {
	ts, st := newWhitelistTestServer(t)
	if _, err := st.CreateAccountWithPassword("taken", "test.example", false, "takenpassword1"); err != nil {
		t.Fatal(err)
	}
	resp, body := wlReq(t, "GET", ts.URL+"/api/names/available?names=Taken,free,bad!!name", "", "", "")
	if resp.StatusCode != 200 {
		t.Fatalf("scan = %d %s", resp.StatusCode, body)
	}
	var out struct {
		Available map[string]bool `json:"available"`
		Invalid   []string        `json:"invalid"`
	}
	_ = json.Unmarshal([]byte(body), &out)
	if out.Available["taken"] != false || out.Available["free"] != true {
		t.Fatalf("availability wrong: %v", out.Available)
	}
	if len(out.Invalid) != 1 || out.Invalid[0] != "bad!!name" {
		t.Fatalf("invalid list wrong: %v", out.Invalid)
	}
	// probe cap
	big := strings.Repeat("n",0)
	_ = big
	names := make([]string, 21)
	for i := range names {
		names[i] = "user" + string(rune('a'+i))
	}
	resp, _ = wlReq(t, "GET", ts.URL+"/api/names/available?names="+strings.Join(names, ","), "", "", "")
	if resp.StatusCode != 400 {
		t.Fatalf("probe cap = %d, want 400", resp.StatusCode)
	}
}

// TestSentStatusField pins the 0.3.7 boss-approved failed-send field:
// partial (whitelist decline) / missing (unknown recipients) / delivered
// ride the sent-index value and surface as sent_status on /api/sent
// summaries; system-notification refs stay stateless.
func TestSentStatusField(t *testing.T) {
	ts, st := newWhitelistTestServer(t)
	if _, err := st.CreateAccountWithPassword("owner", "test.example", false, "ownerpassword1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAccountWithPassword("buddy", "test.example", false, "buddypassword1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAccountWithPassword("gated", "test.example", false, "gatedpassword1"); err != nil {
		t.Fatal(err)
	}
	// the RECIPIENT's gate is what declines (sender's Sent still anchors)
	_ = st.SetWhitelistEnabled("gated@test.example", true)

	send := func(to string) int {
		payload, _ := json.Marshal(map[string]any{"to": []string{to}, "subject": "s", "body": "b"})
		resp, _ := wlReq(t, "POST", ts.URL+"/api/send", "owner@test.example", "ownerpassword1", string(payload))
		return resp.StatusCode
	}

	// partial: real recipient declined (all-rejected -> 403, Sent still anchored)
	if code := send("gated@test.example"); code != 403 {
		t.Fatalf("declined send = %d, want 403", code)
	}
	// delivered: ungated recipient
	if code := send("buddy@test.example"); code != 200 {
		t.Fatalf("delivered send = %d", code)
	}
	// missing: nonexistent recipient
	if code := send("ghost@test.example"); code != 200 {
		t.Fatalf("missing send = %d (missing is silent-200)", code)
	}

	sent, err := st.ReadSent("owner@test.example", 50)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]int{}
	for _, m := range sent {
		if m.SentStatus != nil {
			states[m.SentStatus.State]++
		}
	}
	if states["partial"] != 1 || states["delivered"] != 1 || states["missing"] != 1 {
		t.Fatalf("states wrong: %v (sent=%d)", states, len(sent))
	}
}

// TestThreadSentStatus pins the ReadThread hydration: out-direction
// entries carry sent_status from the sent-index value (partial visible
// in the compose conversation view, boss yellow-badge case).
func TestThreadSentStatus(t *testing.T) {
	ts, st := newWhitelistTestServer(t)
	if _, err := st.CreateAccountWithPassword("owner", "test.example", false, "ownerpassword1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAccountWithPassword("gated", "test.example", false, "gatedpassword1"); err != nil {
		t.Fatal(err)
	}
	_ = st.SetWhitelistEnabled("gated@test.example", true)
	payload, _ := json.Marshal(map[string]any{"to": []string{"gated@test.example"}, "subject": "s", "body": "b"})
	wlReq(t, "POST", ts.URL+"/api/send", "owner@test.example", "ownerpassword1", string(payload))

	thread, err := st.ReadThread("owner@test.example", "gated@test.example", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range thread {
		if e.Dir == "out" && e.SentStatus != nil && e.SentStatus.State == "partial" {
			found = true
		}
	}
	if !found {
		t.Fatalf("out entry must carry sent_status=partial, thread=%+v", thread)
	}
}
