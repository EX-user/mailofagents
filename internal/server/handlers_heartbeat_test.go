package server

import (
	"net/http"
	"testing"
)

// TestWorkerHeartbeat pins the upload/read loop: authenticated write of
// waiting/working state, per-account isolation, and read-back of the
// latest beat.
func TestWorkerHeartbeat(t *testing.T) {
	ts, st := newRegisterTestServer(t)
	addr, pw := mkAccount(t, st, "beater")

	if c := apiCall(t, "POST", ts.URL, "/api/worker/heartbeat", addr, pw,
		`{"state":"working","detail":"s3 watch"}`, nil); c != 200 {
		t.Fatalf("upload = %d, want 200", c)
	}
	var got struct {
		Beat *struct {
			State  string `json:"state"`
			Detail string `json:"detail"`
			At     int64  `json:"at"`
		} `json:"beat"`
	}
	if c := apiCall(t, "GET", ts.URL, "/api/worker/heartbeat", addr, pw, "", &got); c != 200 {
		t.Fatalf("read = %d", c)
	}
	if got.Beat == nil || got.Beat.State != "working" || got.Beat.Detail != "s3 watch" {
		t.Fatalf("beat wrong: %v", got.Beat)
	}

	// Invalid state: 400.
	if c := apiCall(t, "POST", ts.URL, "/api/worker/heartbeat", addr, pw,
		`{"state":"dancing"}`, nil); c != 400 {
		t.Fatalf("invalid state = %d, want 400", c)
	}

	// Unauthenticated: 401 (upload only ever writes the caller's own slot).
	if c := apiCall(t, "POST", ts.URL, "/api/worker/heartbeat", "", "",
		`{"state":"waiting"}`, nil); c != http.StatusUnauthorized {
		t.Fatalf("anon upload = %d, want 401", c)
	}

	// Overwrite semantics: latest wins.
	if c := apiCall(t, "POST", ts.URL, "/api/worker/heartbeat", addr, pw,
		`{"state":"waiting","detail":"idle now"}`, nil); c != 200 {
		t.Fatalf("second upload = %d", c)
	}
	if c := apiCall(t, "GET", ts.URL, "/api/worker/heartbeat", addr, pw, "", &got); c != 200 {
		t.Fatalf("re-read = %d", c)
	}
	if got.Beat == nil || got.Beat.State != "waiting" {
		t.Fatalf("latest beat wrong: %v", got.Beat)
	}

	// Another account starts clean.
	other, otherPw := mkAccount(t, st, "freshbeat")
	var fresh struct {
		Beat *map[string]any `json:"beat"`
	}
	if c := apiCall(t, "GET", ts.URL, "/api/worker/heartbeat", other, otherPw, "", &fresh); c != 200 || fresh.Beat != nil {
		t.Fatalf("fresh read = %d beat=%v, want nil beat", c, fresh.Beat)
	}
}
