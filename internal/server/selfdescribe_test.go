package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestSelfDescribe: the bootstrap endpoint answers publicly, is valid JSON,
// carries the live version + domain, and never leaks account rows.
func TestSelfDescribe(t *testing.T) {
	ts, _ := newSiteCopyServer(t)

	resp, err := http.Get(ts.URL + "/api/self")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var doc map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if doc["version"] == "" || doc["domain"] != "test.example" {
		t.Fatalf("version/domain missing: %+v", doc)
	}
	if _, ok := doc["bootstrap_recipe"]; !ok {
		t.Error("bootstrap_recipe missing")
	}
	if _, ok := doc["mcp"]; !ok {
		t.Error("mcp section missing")
	}
	// POST must be rejected (read-only endpoint).
	req, _ := http.NewRequest("POST", ts.URL+"/api/self", nil)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/self = %d, want 405", resp2.StatusCode)
	}
}
