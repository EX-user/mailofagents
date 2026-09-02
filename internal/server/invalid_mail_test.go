package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/agentmail/agentmail/internal/store"
)

// newInvalidServer: admin + one live user, plus one invalid message seeded
// directly into the store (Send refuses all-invalid deliveries by design).
func newInvalidServer(t *testing.T) (*httptest.Server, *store.Store) {
	ts, st := newSiteCopyServer(t)
	b, err := json.Marshal(store.Message{
		ID:         "01AAAAAAAAAAAAAAAAAAAAAAAA", // 26 chars
		From:       "ghost@example",
		To:         []string{"nosuch@t"},
		Subject:    "invalid one",
		ReceivedAt: 42,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	err = st.DB().Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("messages")).Put([]byte("01AAAAAAAAAAAAAAAAAAAAAAAA"), b)
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return ts, st
}

func TestAdminInvalidListAndDelete(t *testing.T) {
	ts, _ := newInvalidServer(t)

	get := func(user, pass string) (int, string) {
		req, _ := http.NewRequest("GET", ts.URL+"/admin/invalid", nil)
		req.SetBasicAuth(user, pass)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	del := func(user, pass, body string) (int, string) {
		req, _ := http.NewRequest("DELETE", ts.URL+"/admin/invalid", strings.NewReader(body))
		req.SetBasicAuth(user, pass)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("delete: %v", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// Non-admin is shut out of both verbs.
	if code, _ := get("user@test.example", "userpass-123"); code != http.StatusUnauthorized {
		t.Fatalf("non-admin get = %d, want 401", code)
	}
	if code, _ := del("user@test.example", "userpass-123", `{"all":true}`); code != http.StatusUnauthorized {
		t.Fatalf("non-admin delete = %d, want 401", code)
	}

	// Admin list shows the seeded invalid mail.
	code, body := get("admin@test.example", "adminpassword1")
	if code != http.StatusOK {
		t.Fatalf("admin get = %d: %s", code, body)
	}
	var list []map[string]any
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("decode list: %v (%s)", err, body)
	}
	if len(list) != 1 || list[0]["id"] != "01AAAAAAAAAAAAAAAAAAAAAAAA" {
		t.Fatalf("unexpected list: %s", body)
	}

	// Delete by id; the response carries the deleted count.
	code, body = del("admin@test.example", "adminpassword1", `{"ids":["01AAAAAAAAAAAAAAAAAAAAAAAA"]}`)
	if code != http.StatusOK {
		t.Fatalf("admin delete = %d: %s", code, body)
	}
	if !strings.Contains(body, `"deleted":1`) {
		t.Fatalf("delete response = %s, want deleted:1", body)
	}

	// List is empty afterwards; a repeat delete of the same id is a no-op 0.
	if code, body = get("admin@test.example", "adminpassword1"); code != http.StatusOK || strings.TrimSpace(body) != "[]" {
		t.Fatalf("post-delete get = %d %q, want 200 []", code, body)
	}
	if code, body = del("admin@test.example", "adminpassword1", `{"ids":["01AAAAAAAAAAAAAAAAAAAAAAAA"]}`); code != http.StatusOK || !strings.Contains(body, `"deleted":0`) {
		t.Fatalf("idempotent delete = %d %q, want deleted:0", code, body)
	}
}
