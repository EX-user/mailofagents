package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/agentmail/agentmail/internal/audit"
	"github.com/agentmail/agentmail/internal/config"
	"github.com/agentmail/agentmail/internal/store"
)

func newSiteCopyServer(t *testing.T) (*httptest.Server, *store.Store) {
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
		t.Fatalf("audit: %v", err)
	}
	ts := httptest.NewServer(New(st, a, &config.Config{}).Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

// TestSiteCopyAdminFlow: admin PUT -> public GET sees the copy; non-admin
// PUT is forbidden; empty fields leave existing values.
func TestSiteCopyAdminFlow(t *testing.T) {
	ts, st := newSiteCopyServer(t)

	put := func(user, pass, body string) int {
		req, _ := http.NewRequest("PUT", ts.URL+"/admin/site-copy", bytes.NewReader([]byte(body)))
		req.SetBasicAuth(user, pass)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("put: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if code := put("admin@test.example", "adminpassword1",
		`{"portal_tagline_zh":"AI agents 在这里互相写信、协同工作——欢迎围观和加入！","portal_title_en":"Mail of Agents"}`); code != http.StatusOK {
		t.Fatalf("admin put = %d", code)
	}
	if code := put("user@t", "userpass-123", `{"portal_title_en":"hacked"}`); code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Fatalf("non-admin put = %d, want 401/403", code)
	}

	resp, err := http.Get(ts.URL + "/api/site-copy")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("public get = %v %v", resp, err)
	}
	var got map[string]string
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if got["portal_tagline_zh"] == "" || got["portal_title_en"] != "Mail of Agents" {
		t.Fatalf("public copy mismatch: %+v", got)
	}
	// unset fields omitted (empty = default in use)
	if _, ok := got["portal_tagline_en"]; ok {
		t.Fatalf("unset field must be absent: %+v", got)
	}

	// partial update keeps the other field
	if code := put("admin@test.example", "adminpassword1", `{"portal_title_en":"MoA"}`); code != http.StatusOK {
		t.Fatalf("second put = %d", code)
	}
	resp2, _ := http.Get(ts.URL + "/api/site-copy")
	var got2 map[string]string
	json.NewDecoder(resp2.Body).Decode(&got2)
	resp2.Body.Close()
	if got2["portal_tagline_zh"] == "" || got2["portal_title_en"] != "MoA" {
		t.Fatalf("partial update lost fields: %+v", got2)
	}
	_ = st
}
