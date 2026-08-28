package server

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// apiCall issues an authenticated JSON request against the test server and
// decodes the response into out.
func apiCall(t *testing.T, method, url, path, addr, pw, body string, out any) int {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url+path, rd)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if pw != "" {
		req.Header.Set("Authorization", "Basic "+
			base64.StdEncoding.EncodeToString([]byte(addr+":"+pw)))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil {
		_ = json.Unmarshal(raw, out)
	}
	if resp.StatusCode >= 400 {
		t.Logf("%s %s -> %d: %s", method, path, resp.StatusCode, string(raw))
	}
	return resp.StatusCode
}

// TestThreadAPIContract pins the v0.6.15 HTTP surface: send with
// in_reply_to round-trips into summaries and details; bad ULID and
// nonexistent parent 400; /api/threads index; /api/thread?root= mode and
// its mutual exclusion with ?with=.
func TestThreadAPIContract(t *testing.T) {
	ts, st := newRegisterTestServer(t)

	mk := func(name string) (addr, pw string) {
		t.Helper()
		r, err := st.CreateAccount(name, "test.example", false)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return r.Address, r.Password
	}
	alice, alicePw := mk("talice")
	bob, bobPw := mk("tbob")

	send := func(addr, pw, body string, out any) int {
		return apiCall(t, "POST", ts.URL, "/api/send", addr, pw, body, out)
	}

	// Root message.
	var root struct {
		MessageID string `json:"message_id"`
	}
	if code := send(alice, alicePw, `{"to":["`+bob+`"],"subject":"root","body":"x"}`, &root); code != http.StatusOK {
		t.Fatalf("root send = %d", code)
	}

	// Reply carrying in_reply_to round-trips.
	var reply struct {
		MessageID string `json:"message_id"`
	}
	body := `{"to":["` + alice + `"],"subject":"re","body":"x","in_reply_to":"` + root.MessageID + `"}`
	if code := send(bob, bobPw, body, &reply); code != http.StatusOK {
		t.Fatalf("reply send = %d", code)
	}
	var inbox struct {
		Messages []struct {
			ID        string `json:"id"`
			InReplyTo string `json:"in_reply_to"`
		} `json:"messages"`
	}
	apiCall(t, "GET", ts.URL, "/api/inbox?limit=5", alice, alicePw, "", &inbox)
	if len(inbox.Messages) != 1 || inbox.Messages[0].InReplyTo != root.MessageID {
		t.Fatalf("alice inbox summary: %+v", inbox.Messages)
	}
	var detail map[string]any
	apiCall(t, "GET", ts.URL, "/api/message?id="+reply.MessageID, alice, alicePw, "", &detail)
	if detail["in_reply_to"] != root.MessageID {
		t.Fatalf("detail in_reply_to = %v", detail["in_reply_to"])
	}

	// Validation: bad ULID format and nonexistent parent both 400.
	if code := send(alice, alicePw, `{"to":["`+bob+`"],"subject":"x","body":"x","in_reply_to":"not-a-ulid"}`, nil); code != http.StatusBadRequest {
		t.Fatalf("bad ulid = %d, want 400", code)
	}
	if code := send(alice, alicePw, `{"to":["`+bob+`"],"subject":"x","body":"x","in_reply_to":"01ZZZZZZZZZZZZZZZZZZZZZZZZ"}`, nil); code != http.StatusBadRequest {
		t.Fatalf("nonexistent parent = %d, want 400", code)
	}

	// Topic index: one 2-message component, last_at descending.
	var idx struct {
		Threads []struct {
			RootID string `json:"root_id"`
			Count  int    `json:"count"`
		} `json:"threads"`
		Total int `json:"total"`
	}
	if code := apiCall(t, "GET", ts.URL, "/api/threads", alice, alicePw, "", &idx); code != http.StatusOK {
		t.Fatalf("threads = %d", code)
	}
	if idx.Total != 1 || len(idx.Threads) != 1 || idx.Threads[0].Count != 2 || idx.Threads[0].RootID != root.MessageID {
		t.Fatalf("index = %+v", idx)
	}

	// root mode; mutual exclusion; missing param.
	var view struct {
		Root  string `json:"root"`
		Count int    `json:"count"`
	}
	if code := apiCall(t, "GET", ts.URL, "/api/thread?root="+reply.MessageID, alice, alicePw, "", &view); code != http.StatusOK || view.Root != root.MessageID || view.Count != 2 {
		t.Fatalf("root mode = %d view=%+v", code, view)
	}
	if code := apiCall(t, "GET", ts.URL, "/api/thread?with="+bob+"&root="+reply.MessageID, alice, alicePw, "", nil); code != http.StatusBadRequest {
		t.Fatalf("both params = %d, want 400", code)
	}
	if code := apiCall(t, "GET", ts.URL, "/api/thread", alice, alicePw, "", nil); code != http.StatusBadRequest {
		t.Fatalf("no param = %d, want 400", code)
	}
}
