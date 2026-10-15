package server

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestProfileSignatureTriState pins the signature-loss regression: a
// prefs-only (or visible-only) profile POST must NEVER touch the stored
// signature; only an explicitly sent signature field (including "") may.
func TestProfileSignatureTriState(t *testing.T) {
	ts, _ := newRegisterTestServer(t)

	post := func(body string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/profile/self", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.SetBasicAuth("admin@test.example", "adminpassword1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	getSig := func() string {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/profile/self", nil)
		req.SetBasicAuth("admin@test.example", "adminpassword1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out struct {
			Signature string `json:"signature"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out.Signature
	}

	// Set a signature.
	if code := post(`{"signature":"sig-one"}`); code != http.StatusOK {
		t.Fatalf("set signature = %d, want 200", code)
	}
	if s := getSig(); s != "sig-one" {
		t.Fatalf("after set, signature = %q, want sig-one", s)
	}
	// THE REGRESSION: prefs-only POST used to wipe it to "".
	if code := post(`{"prefs":{"audio_autoplay":true}}`); code != http.StatusOK {
		t.Fatalf("prefs-only post = %d, want 200", code)
	}
	if s := getSig(); s != "sig-one" {
		t.Fatalf("after prefs-only post, signature = %q, want sig-one (kept)", s)
	}
	// Visible-only POST is equally harmless.
	if code := post(`{"visible":true}`); code != http.StatusOK {
		t.Fatalf("visible-only post = %d, want 200", code)
	}
	if s := getSig(); s != "sig-one" {
		t.Fatalf("after visible-only post, signature = %q, want sig-one (kept)", s)
	}
	// An explicit empty string still clears (that is a real user intent).
	if code := post(`{"signature":""}`); code != http.StatusOK {
		t.Fatalf("clear signature = %d, want 200", code)
	}
	if s := getSig(); s != "" {
		t.Fatalf("after explicit clear, signature = %q, want empty", s)
	}
}

// TestPrefsLivenessNumericKeys pins the typed whitelist: liveness.weakHours
// /strongHours accept positive numeric hours (and null to remove) and are
// stored; booleans-only keys still reject numbers and vice versa; unknown
// keys stay rejected.
func TestPrefsLivenessNumericKeys(t *testing.T) {
	ts, _ := newRegisterTestServer(t)
	post := func(body string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/profile/self", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.SetBasicAuth("admin@test.example", "adminpassword1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	if code := post(`{"prefs":{"liveness.weakHours":72,"liveness.strongHours":12}}`); code != http.StatusOK {
		t.Fatalf("numeric prefs post = %d, want 200", code)
	}
	if code := post(`{"prefs":{"liveness.weakHours":0}}`); code != http.StatusBadRequest {
		t.Fatalf("zero hours = %d, want 400", code)
	}
	if code := post(`{"prefs":{"liveness.weakHours":"48"}}`); code != http.StatusBadRequest {
		t.Fatalf("string hours = %d, want 400", code)
	}
	if code := post(`{"prefs":{"liveness.weakHours":99999}}`); code != http.StatusBadRequest {
		t.Fatalf("over-cap hours = %d, want 400", code)
	}
	if code := post(`{"prefs":{"audio_autoplay":12}}`); code != http.StatusBadRequest {
		t.Fatalf("number for bool key = %d, want 400", code)
	}
	if code := post(`{"prefs":{"nope":true}}`); code != http.StatusBadRequest {
		t.Fatalf("unknown key = %d, want 400", code)
	}
	if code := post(`{"prefs":{"liveness.weakHours":null}}`); code != http.StatusOK {
		t.Fatalf("null remove = %d, want 200", code)
	}
}

// 0.3.7.4: the mobile onboarding flow persists its per-account "don't show
// again" through prefs.onboarding_dismissed (Iris fa37e42). The key must be
// accepted as a bool, round-trip through GET /api/profile/self, and keep the
// closed-whitelist guarantee (unknown keys still rejected).
func TestOnboardingDismissedPref(t *testing.T) {
	ts, _ := newRegisterTestServer(t)
	h := map[string][]string{"Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte("admin@test.example:adminpassword1"))}}

	post := func(body string) int {
		req, _ := http.NewRequest("POST", ts.URL+"/api/profile/self", strings.NewReader(body))
		req.Header = h
		req.Header.Set("Content-Type", "application/json")
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	// set the key
	if code := post(`{"prefs":{"onboarding_dismissed":true}}`); code != http.StatusOK {
		t.Fatalf("onboarding_dismissed post = %d, want 200", code)
	}
	// round-trip
	req, _ := http.NewRequest("GET", ts.URL+"/api/profile/self", nil)
	req.Header = h
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	prefs, _ := got["prefs"].(map[string]any)
	if prefs == nil || prefs["onboarding_dismissed"] != true {
		t.Fatalf("prefs round-trip missing onboarding_dismissed=true: %v", got["prefs"])
	}
	// clear via null
	if code := post(`{"prefs":{"onboarding_dismissed":null}}`); code != http.StatusOK {
		t.Fatalf("null clear = %d, want 200", code)
	}
	// wrong type rejected
	if code := post(`{"prefs":{"onboarding_dismissed":"yes"}}`); code != http.StatusBadRequest {
		t.Fatalf("string value = %d, want 400", code)
	}
	// unknown keys stay rejected (closed whitelist preserved)
	if code := post(`{"prefs":{"onboarding_step":2}}`); code != http.StatusBadRequest {
		t.Fatalf("unknown key = %d, want 400", code)
	}
}
