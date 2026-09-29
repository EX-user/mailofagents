package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentmail/agentmail/internal/audit"
	"github.com/agentmail/agentmail/internal/config"
	"github.com/agentmail/agentmail/internal/store"
)

// TestInboxBadgeSkipsAudit (red-dot poll exemption, boss 09-29): the badge
// poll (GET /api/inbox?limit=1&badge=1) fires on a per-second cadence and
// must NOT bloat the audit log; every ordinary inbox read still records.
func TestInboxBadgeSkipsAudit(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.DB().Close()
	if err := st.BootstrapSystem("admin", "adminpassword1", "test.example"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	a, err := audit.New(st.DB())
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	ts := httptest.NewServer(New(st, a, &config.Config{}).Handler())
	defer ts.Close()

	// Bootstrap two accounts and put one letter in the inbox.
	const testPass = "pw-test-1234"
	reg := func(name string) string {
		body := `{"name":"` + name + `","password":"` + testPass + `"}`
		req, _ := http.NewRequest("POST", ts.URL+"/api/register", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("register: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("register status = %d", resp.StatusCode)
		}
		var out struct {
			Address string `json:"address"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode register: %v", err)
		}
		return out.Address
	}
	senderAddr := reg("badgesender")
	recvAddr := reg("badgerecv")
	recvPass := testPass

	sendReq, _ := http.NewRequest("POST", ts.URL+"/api/send", strings.NewReader(
		`{"to":["`+recvAddr+`"],"subject":"badge probe","body":"b"}`))
	sendReq.Header.Set("Content-Type", "application/json")
	sendReq.SetBasicAuth(senderAddr, testPass)
	sendResp, err := http.DefaultClient.Do(sendReq)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	sendResp.Body.Close()
	if sendResp.StatusCode != http.StatusOK {
		t.Fatalf("send status = %d", sendResp.StatusCode)
	}

	get := func(path string) *http.Response {
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		req.SetBasicAuth(recvAddr, recvPass)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d", path, resp.StatusCode)
		}
		return resp
	}

	// Badge poll: recorded audit entries must stay flat.
	get("/api/inbox?limit=1&badge=1")
	get("/api/inbox?limit=1&badge=1")
	get("/api/inbox?limit=1&badge=1")
	entries, err := a.List(nil, 100)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	badgeReads := 0
	for _, e := range entries {
		if e.Action == audit.ActionReadInbox {
			badgeReads++
		}
	}
	if badgeReads != 0 {
		t.Fatalf("badge=1 recorded %d ReadInbox audit entries, want 0", badgeReads)
	}

	// Ordinary read (default limit, no badge): exactly one audit entry.
	get("/api/inbox?limit=1")
	entries, err = a.List(nil, 100)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	ordinaryReads := 0
	for _, e := range entries {
		if e.Action == audit.ActionReadInbox {
			ordinaryReads++
		}
	}
	if ordinaryReads != 1 {
		t.Fatalf("ordinary inbox read recorded %d ReadInbox entries, want 1", ordinaryReads)
	}
}
