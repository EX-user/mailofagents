package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentmail/agentmail/internal/store"
)

// postLimit issues POST /api/account/limits authenticated as authAddr and
// returning the status code, decoding the response body into out.
func postLimit(t *testing.T, ts *httptest.Server, authAddr, authPw, body string, out any) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/account/limits", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(authAddr, authPw)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil {
		_ = json.Unmarshal(raw, out)
	}
	return resp.StatusCode
}

func getLimit(t *testing.T, ts *httptest.Server, authAddr, authPw, query string, out any) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/account/limits"+query, nil)
	req.SetBasicAuth(authAddr, authPw)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil {
		_ = json.Unmarshal(raw, out)
	}
	return resp.StatusCode
}

// TestRecipientLimitsLifecycle pins the boss bloat-lever contract
// (2026-09-09): defaults unlimited; self and direct superior can view and
// adjust; unrelated accounts are locked out; the send path enforces the
// caps first with a plain count-vs-limit message; 0 lifts the cap again.
func TestRecipientLimitsLifecycle(t *testing.T) {
	ts, st := newRegisterTestServer(t)

	mk := func(name string) (addr, pw string) {
		t.Helper()
		r, err := st.CreateAccount(name, "test.example", false)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return r.Address, r.Password
	}
	boss, bossPw := mk("super") // superior end
	work, workPw := mk("worker")
	peer, peerPw := mk("peer") // unrelated account
	if err := st.DeclareSubordinate(boss, work); err != nil {
		t.Fatalf("declare: %v", err)
	}
	// Real recipient accounts — unknown addresses are dropped at delivery,
	// so the within-caps send needs accounts that exist.
	dest1, _ := mk("dest1")
	dest2, _ := mk("dest2")
	copied, _ := mk("copied")

	// Defaults: unlimited (0), visible to the account itself.
	var got struct {
		Address       string `json:"address"`
		MaxRecipients int    `json:"max_recipients"`
		MaxCC         int    `json:"max_cc"`
	}
	if code := getLimit(t, ts, work, workPw, "", &got); code != http.StatusOK {
		t.Fatalf("self GET = %d, want 200", code)
	}
	if got.MaxRecipients != 0 || got.MaxCC != 0 {
		t.Fatalf("defaults = %+v, want 0/0 (unlimited)", got)
	}

	// Superior adjusts the subordinate's caps.
	var setRes struct {
		OK            bool `json:"ok"`
		MaxRecipients int  `json:"max_recipients"`
		MaxCC         int  `json:"max_cc"`
	}
	body := fmt.Sprintf(`{"address":%q,"max_recipients":2,"max_cc":1}`, work)
	if code := postLimit(t, ts, boss, bossPw, body, &setRes); code != http.StatusOK {
		t.Fatalf("superior POST = %d, want 200", code)
	}
	if !setRes.OK || setRes.MaxRecipients != 2 || setRes.MaxCC != 1 {
		t.Fatalf("set response = %+v", setRes)
	}
	if acc, _ := st.GetAccount(work); acc.MaxRecipients != 2 || acc.MaxCC != 1 {
		t.Fatalf("stored caps = %d/%d, want 2/1", acc.MaxRecipients, acc.MaxCC)
	}

	// Send path: 2 recipients + 1 cc pass, over-cap fails first with a
	// plain message (the limit gate sits before every other validation).
	send := func(tos, ccs []string) (int, string) {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{"to": tos, "cc": ccs, "subject": "s", "body": "b"})
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/send", strings.NewReader(string(payload)))
		req.SetBasicAuth(work, workPw)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	if code, msg := send([]string{dest1, dest2}, []string{copied}); code != http.StatusOK {
		t.Fatalf("send within caps = %d (%s), want 200", code, msg)
	}
	code, msg := send([]string{dest1, dest2, copied}, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("send over to-cap = %d, want 400", code)
	}
	if !strings.Contains(msg, "too many recipients: 3 given, limit is 2") {
		t.Fatalf("over-cap message = %q, want plain count-vs-limit", msg)
	}
	code, msg = send([]string{dest1}, []string{dest2, copied})
	if code != http.StatusBadRequest {
		t.Fatalf("send over cc-cap = %d, want 400", code)
	}
	if !strings.Contains(msg, "too many cc: 2 given, limit is 1") {
		t.Fatalf("cc-cap message = %q", msg)
	}

	// The subordinate adjusts its own caps (self-serve), lifting to/cc via 0.
	if code := postLimit(t, ts, work, workPw, `{"max_recipients":0}`, &setRes); code != http.StatusOK {
		t.Fatalf("self POST = %d, want 200", code)
	}
	if acc, _ := st.GetAccount(work); acc.MaxRecipients != 0 || acc.MaxCC != 1 {
		t.Fatalf("after self-set caps = %d/%d, want 0/1", acc.MaxRecipients, acc.MaxCC)
	}
	if code, msg := send([]string{dest1, dest2}, []string{copied}); code != http.StatusOK {
		t.Fatalf("send after lift = %d (%s), want 200", code, msg)
	}

	// Lockout: an unrelated account can neither view nor adjust; the
	// subordinate cannot manage its superior either (direction is
	// superior→subordinate only).
	if code := getLimit(t, ts, peer, peerPw, "?address="+work, nil); code != http.StatusForbidden {
		t.Fatalf("peer GET = %d, want 403", code)
	}
	if code := postLimit(t, ts, peer, peerPw, fmt.Sprintf(`{"address":%q,"max_cc":5}`, work), nil); code != http.StatusForbidden {
		t.Fatalf("peer POST = %d, want 403", code)
	}
	if code := postLimit(t, ts, work, workPw, fmt.Sprintf(`{"address":%q,"max_cc":5}`, boss), nil); code != http.StatusForbidden {
		t.Fatalf("sub→superior POST = %d, want 403", code)
	}

	// Admin overrides anything; range validation rejects junk.
	if code := postLimit(t, ts, "admin@test.example", "adminpassword1", fmt.Sprintf(`{"address":%q,"max_recipients":9,"max_cc":9}`, work), nil); code != http.StatusOK {
		t.Fatalf("admin POST = %d, want 200", code)
	}
	if code := postLimit(t, ts, work, workPw, `{"max_cc":-1}`, nil); code != http.StatusBadRequest {
		t.Fatalf("negative POST = %d, want 400", code)
	}
	if code := postLimit(t, ts, work, workPw, fmt.Sprintf(`{"max_recipients":%d}`, store.MaxRecipientLimitCap+1), nil); code != http.StatusBadRequest {
		t.Fatalf("over-cap POST = %d, want 400", code)
	}
}
