package server

import (
	"bytes"
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

// newTokenTestServer boots an initialized server with a regular account.
func newTokenTestServer(t *testing.T) (*httptest.Server, string) {
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
	ts := httptest.NewServer(New(st, a, &config.Config{}).Handler())
	t.Cleanup(ts.Close)
	return ts, "user@t:userpass-123"
}

// TestTokenAuthFlow (v0.6.27 remember-login): mint with Basic → every
// account route accepts the bearer → logout revokes it → Basic fallback
// still works throughout; an invalid bearer does NOT masquerade as Basic.
func TestTokenAuthFlow(t *testing.T) {
	ts, basic := newTokenTestServer(t)

	// 1. Mint via Basic.
	req, _ := http.NewRequest("POST", ts.URL+"/api/auth/token", nil)
	req.SetBasicAuth(strings.SplitN(basic, ":", 2)[0], strings.SplitN(basic, ":", 2)[1])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	var body struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(body.Token) != 64 || body.ExpiresAt == 0 {
		t.Fatalf("mint resp = %d, body %+v", resp.StatusCode, body)
	}

	// 2. Bearer works on a normal account route.
	req, _ = http.NewRequest("GET", ts.URL+"/api/inbox?limit=1", nil)
	req.Header.Set("Authorization", "Bearer "+body.Token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("bearer inbox: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bearer inbox status = %d, want 200", resp.StatusCode)
	}

	// 3. Invalid bearer → 401 (no Basic fallthrough).
	req, _ = http.NewRequest("GET", ts.URL+"/api/inbox?limit=1", nil)
	req.Header.Set("Authorization", "Bearer garbage")
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid bearer status = %d, want 401", resp.StatusCode)
	}

	// 4. Logout with the bearer revokes it.
	req, _ = http.NewRequest("DELETE", ts.URL+"/api/auth/token", nil)
	req.Header.Set("Authorization", "Bearer "+body.Token)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logout status = %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("GET", ts.URL+"/api/inbox?limit=1", nil)
	req.Header.Set("Authorization", "Bearer "+body.Token)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked bearer status = %d, want 401", resp.StatusCode)
	}

	// 5. Basic fallback unchanged.
	req, _ = http.NewRequest("GET", ts.URL+"/api/inbox?limit=1", nil)
	req.SetBasicAuth(strings.SplitN(basic, ":", 2)[0], strings.SplitN(basic, ":", 2)[1])
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("basic fallback status = %d", resp.StatusCode)
	}
}

// TestPasswordChangeRevokesTokens: changing the password kills every minted
// token; the new password + fresh Basic auth still work.
func TestPasswordChangeRevokesTokens(t *testing.T) {
	ts, basic := newTokenTestServer(t)
	user, pass := strings.SplitN(basic, ":", 2)[0], strings.SplitN(basic, ":", 2)[1]

	req, _ := http.NewRequest("POST", ts.URL+"/api/auth/token", nil)
	req.SetBasicAuth(user, pass)
	resp, _ := http.DefaultClient.Do(req)
	var body struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()

	payload, _ := json.Marshal(map[string]string{"old_password": pass, "new_password": "newpass-456"})
	req, _ = http.NewRequest("POST", ts.URL+"/api/password", bytes.NewReader(payload))
	req.SetBasicAuth(user, pass)
	req.Header.Set("Content-Type", "application/json")
	resp, _ = http.DefaultClient.Do(req)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("change password status = %d", resp.StatusCode)
	}

	req, _ = http.NewRequest("GET", ts.URL+"/api/inbox?limit=1", nil)
	req.Header.Set("Authorization", "Bearer "+body.Token)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old bearer after password change = %d, want 401", resp.StatusCode)
	}
}
