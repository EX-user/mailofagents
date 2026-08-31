package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestProfileAlias pins the /api/profile short alias: it must behave exactly
// like /api/profile/self (same handler, same tri-state semantics) — visible
// and signature take effect immediately, omitted fields keep stored values,
// an explicit "" clears the signature, and both paths read the same resource.
func TestProfileAlias(t *testing.T) {
	ts, _ := newRegisterTestServer(t)

	call := func(method, path, body string) map[string]any {
		t.Helper()
		var req *http.Request
		var err error
		if body == "" {
			req, err = http.NewRequest(method, ts.URL+path, nil)
		} else {
			req, err = http.NewRequest(method, ts.URL+path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
		if err != nil {
			t.Fatal(err)
		}
		req.SetBasicAuth("admin@test.example", "adminpassword1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s = %d, want 200 (body %s)", method, path, resp.StatusCode, raw)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s %s: bad JSON %q: %v", method, path, raw, err)
		}
		return out
	}

	// Set through the alias: signature is trimmed on the server.
	out := call(http.MethodPost, "/api/profile", `{"visible":true,"signature":"  hello sig  "}`)
	if out["signature"] != "hello sig" {
		t.Fatalf("alias post signature = %v, want trimmed 'hello sig'", out["signature"])
	}
	// Both paths read the same stored resource.
	for _, p := range []string{"/api/profile", "/api/profile/self"} {
		got := call(http.MethodGet, p, "")
		if got["visible"] != true || got["signature"] != "hello sig" {
			t.Fatalf("GET %s -> visible=%v signature=%v, want true/'hello sig'", p, got["visible"], got["signature"])
		}
	}
	// Omitted visible must NOT un-list (tri-state), explicit "" clears sig.
	call(http.MethodPost, "/api/profile", `{"signature":""}`)
	got := call(http.MethodGet, "/api/profile", "")
	if got["visible"] != true {
		t.Fatalf("sig-only post reset visible to %v, want kept true", got["visible"])
	}
	if got["signature"] != "" {
		t.Fatalf("explicit clear left signature %v, want empty", got["signature"])
	}
	// Un-listing works through the alias too.
	call(http.MethodPost, "/api/profile", `{"visible":false}`)
	got = call(http.MethodGet, "/api/profile/self", "")
	if got["visible"] != false {
		t.Fatalf("visible=false via alias did not stick: %v", got["visible"])
	}
}
