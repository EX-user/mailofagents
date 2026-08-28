package server

import (
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

// newRegisterTestServer boots a fully initialized server backed by a
// temp store (admin bootstrapped, domain test.example).
func newRegisterTestServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
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

// TestRegisterPasswordlessGatedByRandomToggle pins the retirement of the
// one-click random register: the passwordless path is 403 by default,
// works only while the admin debug toggle is on, and password register is
// never affected.
func TestRegisterPasswordlessGatedByRandomToggle(t *testing.T) {
	ts, st := newRegisterTestServer(t)

	post := func(body string) int {
		t.Helper()
		resp, err := http.Post(ts.URL+"/api/register", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("post register: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	// Default OFF (superior directive: the mechanism retired).
	if code := post(`{"name":"pwless-default"}`); code != http.StatusForbidden {
		t.Fatalf("passwordless register default = %d, want 403", code)
	}
	// Admin re-enables the debug path -> works again.
	if err := st.SetRandomRegisterEnabled(true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if code := post(`{"name":"pwless-debug"}`); code != http.StatusOK {
		t.Fatalf("passwordless register with toggle on = %d, want 200", code)
	}
	// Password register is unaffected in either state.
	if code := post(`{"name":"normal-account","password":"password123"}`); code != http.StatusOK {
		t.Fatalf("password register = %d, want 200", code)
	}
	// Toggle back off -> gate returns.
	if err := st.SetRandomRegisterEnabled(false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if code := post(`{"name":"pwless-again"}`); code != http.StatusForbidden {
		t.Fatalf("passwordless register after disable = %d, want 403", code)
	}
}

// TestRegisterSubordinateNamed pins the v0.6.4 contract: an optional
// {username} names the subordinate; a taken name answers 409 (no silent
// rename); an absent/empty body keeps the random bot- path.
func TestRegisterSubordinateNamed(t *testing.T) {
	ts, st := newRegisterTestServer(t)
	// Six registration attempts exceed the default 5/hour per-IP throttle;
	// 0 disables it for this test.
	_ = st.SetRegisterIPRateLimit(0)
	// Bootstrap admin already owns admin@test.example.
	post := func(body string, user string) int {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/register-subordinate", rd)
		req.Header.Set("Content-Type", "application/json")
		req.SetBasicAuth(user, "adminpassword1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	// Named creation succeeds and uses the exact name.
	if code := post(`{"username":"my-agent"}`, "admin@test.example"); code != http.StatusCreated {
		t.Fatalf("named register = %d, want 201", code)
	}
	if _, err := st.GetAccount("my-agent@test.example"); err != nil {
		t.Errorf("my-agent not created: %v", err)
	}
	// Same name again -> 409, original untouched.
	if code := post(`{"username":"my-agent"}`, "admin@test.example"); code != http.StatusConflict {
		t.Fatalf("taken name = %d, want 409", code)
	}
	// Existing top-level account name also conflicts.
	if code := post(`{"username":"admin"}`, "admin@test.example"); code != http.StatusConflict {
		t.Fatalf("existing name = %d, want 409", code)
	}
	// Charset and length validation.
	if code := post(`{"username":"bad name!"}`, "admin@test.example"); code != http.StatusBadRequest {
		t.Fatalf("bad charset = %d, want 400", code)
	}
	if code := post(`{"username":"`+strings.Repeat("a", 33)+`"}`, "admin@test.example"); code != http.StatusBadRequest {
		t.Fatalf("too long = %d, want 400", code)
	}
	// Empty body keeps the random path.
	if code := post("", "admin@test.example"); code != http.StatusCreated {
		t.Fatalf("random register = %d, want 201", code)
	}
}
