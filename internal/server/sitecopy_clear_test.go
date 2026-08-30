package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestSiteCopyClearRestoresDefault: v0.1.5 "empty = reset to default" — a
// key PUT as "" clears the override (built-in default shows through again),
// an ABSENT key leaves its value untouched, unknown keys are rejected.
func TestSiteCopyClearRestoresDefault(t *testing.T) {
	ts, _ := newSiteCopyServer(t)

	put := func(user, pass, body string) (int, string) {
		req, _ := http.NewRequest("PUT", ts.URL+"/admin/site-copy", strings.NewReader(body))
		req.SetBasicAuth(user, pass)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("put: %v", err)
		}
		defer resp.Body.Close()
		buf := make([]byte, 200)
		n, _ := resp.Body.Read(buf)
		return resp.StatusCode, string(buf[:n])
	}
	get := func() map[string]string {
		resp, err := http.Get(ts.URL + "/api/site-copy")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("public get = %d", resp.StatusCode)
		}
		var got map[string]string
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return got
	}

	// Configure two keys.
	if code, _ := put("admin@test.example", "adminpassword1",
		`{"portal_tagline_en":"Custom tag","panel_title_en":"Custom panel"}`); code != http.StatusOK {
		t.Fatalf("config put = %d", code)
	}
	if got := get(); got["portal_tagline_en"] != "Custom tag" || got["panel_title_en"] != "Custom panel" {
		t.Fatalf("config mismatch: %+v", got)
	}

	// Clear one via empty value; the other (absent from the body) survives.
	if code, _ := put("admin@test.example", "adminpassword1", `{"portal_tagline_en":""}`); code != http.StatusOK {
		t.Fatalf("clear put = %d", code)
	}
	got := get()
	if _, ok := got["portal_tagline_en"]; ok {
		t.Fatalf("cleared key still present: %+v", got)
	}
	if got["panel_title_en"] != "Custom panel" {
		t.Fatalf("absent key lost: %+v", got)
	}

	// Unknown keys are rejected.
	if code, _ := put("admin@test.example", "adminpassword1", `{"bogus_key":"x"}`); code != http.StatusBadRequest {
		t.Fatalf("unknown key = %d, want 400", code)
	}
	// Non-string values are rejected.
	if code, _ := put("admin@test.example", "adminpassword1", `{"portal_tagline_en":42}`); code != http.StatusBadRequest {
		t.Fatalf("non-string value = %d, want 400", code)
	}
	// Length cap still enforced on the set path.
	long := strings.Repeat("x", 201)
	if code, _ := put("admin@test.example", "adminpassword1", `{"portal_tagline_en":"`+long+`"}`); code != http.StatusBadRequest {
		t.Fatalf("oversize = %d, want 400", code)
	}
}
