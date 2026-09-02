package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentmail/agentmail/internal/audit"
	"github.com/agentmail/agentmail/internal/config"
	"github.com/agentmail/agentmail/internal/store"
)

// Gate probes for 0.2.4 (weber triage ②③, Devi plan adopted by alice):
//  1. OPTIONS is rejected outright with 405 — the ServeMux is method-blind
//     and OPTIONS used to EXECUTE the GET handler and return data.
//  2. Unmatched /api/* paths answer a plain-text "not found" 404 — no Go
//     default page on the API face.
func TestOptionsRejectedAndAPIFallback404(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.DB().Close()
	if err := st.BootstrapSystem("admin", "adminpassword1", "test.example"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	auditSvc, err := audit.New(st.DB())
	if err != nil {
		t.Fatalf("open audit: %v", err)
	}
	ts := httptest.NewServer(New(st, auditSvc, &config.Config{}).Handler())
	defer ts.Close()

	// OPTIONS anywhere → 405, never data.
	resp, err := http.NewRequest(http.MethodOptions, ts.URL+"/api/inbox", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	got, err := http.DefaultClient.Do(resp)
	if err != nil {
		t.Fatalf("options: %v", err)
	}
	body, _ := io.ReadAll(got.Body)
	got.Body.Close()
	if got.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("OPTIONS /api/inbox: got %d, want 405", got.StatusCode)
	}
	if strings.Contains(strings.ToLower(string(body)), `"messages"`) {
		t.Errorf("OPTIONS leaked inbox data: %q", string(body))
	}

	// Unknown /api/* → 404 plain text (no Go default page, no HTML).
	resp2, err := http.Get(ts.URL + "/api/definitely-not-a-route")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("unknown api path: got %d, want 404", resp2.StatusCode)
	}
	if !strings.HasPrefix(string(body2), "not found") {
		t.Errorf("unknown api path body = %q, want plain \"not found\"", string(body2))
	}
	if strings.Contains(string(body2), "<") {
		t.Errorf("unknown api path body looks like a page: %q", string(body2))
	}
}
