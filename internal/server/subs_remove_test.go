package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/agentmail/agentmail/internal/audit"
)

// postRemove issues a POST /api/subs/remove authenticated as authAddr and
// targeting target in the JSON body, decoding the response into out.
func postRemove(t *testing.T, url, authAddr, authPw, target string, out any) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url+"/api/subs/remove",
		strings.NewReader(`{"address":"`+target+`"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if authPw != "" {
		req.Header.Set("Authorization", "Basic "+
			base64.StdEncoding.EncodeToString([]byte(authAddr+":"+authPw)))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post remove: %v", err)
	}
	defer resp.Body.Close()
	if out != nil {
		raw, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(raw, out)
	}
	return resp.StatusCode
}

// TestSubsRemoveBidirectional pins the v0.6.5 contract (alice 01M0VCEDB):
// either end of the unique edge may remove it, the response names the
// initiator's role, the other party receives a system notification mail in
// the same transaction, missing relationships 404, and the derived mgmt
// overview reflects the removal immediately (bSubs scan, zero cleanup).
func TestSubsRemoveBidirectional(t *testing.T) {
	ts, st := newRegisterTestServer(t)

	mk := func(name string) (addr, pw string) {
		t.Helper()
		r, err := st.CreateAccount(name, "test.example", false)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return r.Address, r.Password
	}
	boss, bossPw := mk("boss")   // superior end
	work, workPw := mk("worker") // subordinate end
	if err := st.DeclareSubordinate(boss, work); err != nil {
		t.Fatalf("declare: %v", err)
	}
	if !st.IsSubordinate(boss, work) {
		t.Fatal("precondition: edge missing")
	}

	// 1. Subordinate end removes: role=subordinate, edge gone.
	var res struct {
		Removed        bool   `json:"removed"`
		InitiatorRole  string `json:"initiator_role"`
	}
	if code := postRemove(t, ts.URL, work, workPw, boss, &res); code != http.StatusOK {
		t.Fatalf("subordinate-end remove = %d, want 200", code)
	}
	if !res.Removed || res.InitiatorRole != "subordinate" {
		t.Fatalf("got %+v, want removed=true initiator_role=subordinate", res)
	}
	if st.IsSubordinate(boss, work) {
		t.Fatal("edge still present after removal")
	}

	// 2. Notification mail landed in the other party's inbox, sender =
	// remover, subject carries both addresses; remover has a sent copy.
	inbox, err := st.ReadInbox(boss, 10)
	if err != nil {
		t.Fatalf("read boss inbox: %v", err)
	}
	if len(inbox) != 1 {
		t.Fatalf("boss inbox = %d messages, want the single notification", len(inbox))
	}
	n := inbox[0]
	if n.From != work || !strings.HasPrefix(n.Subject, "[从属关系解除]") {
		t.Fatalf("notification from=%q subject=%q", n.From, n.Subject)
	}
	if !strings.Contains(n.Subject, boss) || !strings.Contains(n.Subject, work) {
		t.Fatalf("subject %q lacks both addresses", n.Subject)
	}
	full, err := st.GetMessage(boss, n.ID)
	if err != nil {
		t.Fatalf("get notification: %v", err)
	}
	if !strings.Contains(full.Body, boss) || !strings.Contains(full.Body, "系统代发") {
		t.Fatalf("body %q lacks standard sentence", full.Body)
	}
	sent, err := st.ReadSent(work, 10)
	if err != nil || len(sent) != 1 {
		t.Fatalf("remover sent copy: %d msgs, err=%v", len(sent), err)
	}

	// 3. Superior end removes a fresh pair: role=superior.
	chief, chiefPw := mk("chief")
	member, _ := mk("member")
	if err := st.DeclareSubordinate(chief, member); err != nil {
		t.Fatalf("declare pair2: %v", err)
	}
	if code := postRemove(t, ts.URL, chief, chiefPw, member, &res); code != http.StatusOK {
		t.Fatalf("superior-end remove = %d, want 200", code)
	}
	if !res.Removed || res.InitiatorRole != "superior" {
		t.Fatalf("got %+v, want removed=true initiator_role=superior", res)
	}
	if st.IsSubordinate(chief, member) {
		t.Fatal("pair2 edge still present")
	}
	// The member also got notified.
	memberInbox, err := st.ReadInbox(member, 10)
	if err != nil || len(memberInbox) != 1 {
		t.Fatalf("member notification: %d msgs, err=%v", len(memberInbox), err)
	}

	// 4. Missing relationship (either direction) -> 404, no state change.
	if code := postRemove(t, ts.URL, boss, bossPw, work, nil); code != http.StatusNotFound {
		t.Fatalf("no-relationship remove = %d, want 404", code)
	}
	if code := postRemove(t, ts.URL, work, workPw, work, nil); code != http.StatusNotFound {
		t.Fatalf("self remove = %d, want 404", code)
	}

	// 5. Auth required.
	if code := postRemove(t, ts.URL, work, "", boss, nil); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated remove = %d, want 401", code)
	}

	// 6. Derived mgmt views clean immediately: overview for a superior
	// loses the subordinate (subs list, graph nodes and edges) with zero
	// cleanup — everything scans bSubs live.
	ov, err := st.MgmtSubsOverview(chief)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	for _, s := range ov.Subs {
		if s.Address == member {
			t.Fatalf("overview still lists removed sub %s", member)
		}
	}
	for _, node := range ov.Graph.Nodes {
		if node.Address == member {
			t.Fatalf("graph still has node %s after removal", member)
		}
	}
	for _, e := range ov.Graph.Edges {
		if e.A == member || e.B == member {
			t.Fatalf("graph still has edge %+v after removal", e)
		}
	}

	// 7. Audit recorded: sub_removed with initiator role.
	au, err := audit.New(st.DB())
	if err != nil {
		t.Fatalf("open audit: %v", err)
	}
	entries, err := au.List(context.Background(), 20)
	if err != nil {
		t.Fatalf("audit list: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Action == audit.ActionSubRemoved && strings.Contains(e.Detail, "initiator_role=superior") {
			found = true
		}
	}
	if !found {
		t.Fatal("sub_removed audit entry with role missing")
	}
}
