package server

import (
	"net/http"
	"testing"
)

// TestAdminBearerTokenPassesAdminGate: remember-me admins call /admin/*
// with a Bearer session token; the Basic-only gate 401'd them and the panel
// bounced back to login (v0.1.3 P1). Bearer admin -> 200, bogus -> 401,
// Bearer regular -> 401.
func TestAdminBearerTokenPassesAdminGate(t *testing.T) {
	ts, st := newSiteCopyServer(t)

	tok, _, err := st.CreateSessionToken("admin@test.example")
	if err != nil {
		t.Fatalf("admin token: %v", err)
	}
	if _, err := st.CreateAccountWithPassword("regular", "t", false, "regularpass-1"); err != nil {
		t.Fatalf("register regular: %v", err)
	}
	rtok, _, err := st.CreateSessionToken("regular@t")
	if err != nil {
		t.Fatalf("regular token: %v", err)
	}

	get := func(auth string) int {
		req, _ := http.NewRequest("GET", ts.URL+"/admin/stats", nil)
		req.Header.Set("Authorization", auth)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if code := get("Bearer " + tok); code != http.StatusOK {
		t.Fatalf("admin bearer /admin/stats = %d, want 200", code)
	}
	if code := get("Bearer bogus-token"); code != http.StatusUnauthorized {
		t.Fatalf("bogus bearer = %d, want 401", code)
	}
	if code := get("Bearer " + rtok); code != http.StatusUnauthorized {
		t.Fatalf("regular bearer /admin/stats = %d, want 401", code)
	}
}
