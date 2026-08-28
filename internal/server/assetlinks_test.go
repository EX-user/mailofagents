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

// TestAssetlinksRoute (v0.6.26 TWA): /.well-known/assetlinks.json is served
// anonymously with the embedded statement — Android's domain verifier
// fetches it without credentials, and the embed (not an ops-managed file)
// guarantees a server swap can never lose it.
func TestAssetlinksRoute(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/test.db")
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

	resp, err := http.Get(ts.URL + "/.well-known/assetlinks.json")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"namespace": "android_app"`) {
		t.Errorf("body does not look like an assetlinks statement: %.120s", body)
	}
	if !strings.Contains(string(body), `"package_name": "online.mailofagents.twa"`) {
		t.Errorf("statement missing the TWA package name: %.120s", body)
	}
	// Non-GET is rejected.
	resp2, err := http.Post(ts.URL+"/.well-known/assetlinks.json", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", resp2.StatusCode)
	}
}
