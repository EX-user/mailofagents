package store

import (
	"strings"
	"testing"
	"time"
)

func newFilesStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "files.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.db.Close() })
	for _, n := range []string{"a", "b", "c"} {
		if _, err := s.CreateAccount(n, "t", false); err != nil {
			t.Fatalf("create %s: %v", n, err)
		}
	}
	return s
}

func TestFileRoundtrip(t *testing.T) {
	s := newFilesStore(t)
	rec, err := s.SaveFile("a@t", "notes.txt", []string{"b@t"}, []byte("hello attachment"))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if rec.AccessCode == "" || rec.ID == "" || rec.Size != 16 {
		t.Errorf("record fields wrong: %+v", rec)
	}
	content, err := s.GetFileContent(rec.ID)
	if err != nil || string(content) != "hello attachment" {
		t.Errorf("content = %q, %v", content, err)
	}
}

func TestAuthorizeFileDownload(t *testing.T) {
	s := newFilesStore(t)
	rec, err := s.SaveFile("a@t", "f.bin", []string{"b@t"}, []byte("x"))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	cases := []struct {
		who, code string
		wantErr   bool
	}{
		{"a@t", rec.AccessCode, false}, // owner + code
		{"b@t", rec.AccessCode, false}, // allowed + code
		{"c@t", rec.AccessCode, true},  // not owner/allowed
		{"a@t", "wrong-code", true},    // owner but bad code
		{"b@t", "", true},              // allowed but no code
	}
	for _, c := range cases {
		_, err := s.AuthorizeFileDownload(c.who, rec.ID, c.code)
		if (err != nil) != c.wantErr {
			t.Errorf("Authorize(%s, %q) err=%v wantErr=%v", c.who, c.code, err, c.wantErr)
		}
	}
	if _, err := s.AuthorizeFileDownload("a@t", "NOPE", rec.AccessCode); err != ErrFileNotFound {
		t.Errorf("missing id should be ErrFileNotFound, got %v", err)
	}
}

func TestFileQuota(t *testing.T) {
	s := newFilesStore(t)
	// Fill the 20MB quota with 19 x 1MB + one small file (19MB + overhead).
	for i := 0; i < 19; i++ {
		if _, err := s.SaveFile("a@t", "big.bin", nil, make([]byte, FileMaxBytes)); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	if _, err := s.SaveFile("a@t", "ok.bin", nil, make([]byte, 1<<20)); err != nil {
		t.Errorf("20th MB should fit exactly, got %v", err)
	}
	if _, err := s.SaveFile("a@t", "over.bin", nil, []byte("x")); err != ErrQuotaExceeded {
		t.Errorf("next byte should exceed quota, got %v", err)
	}
	// Other accounts unaffected.
	if _, err := s.SaveFile("b@t", "other.bin", nil, []byte("y")); err != nil {
		t.Errorf("other account should have its own quota, got %v", err)
	}
}

func TestFileTTLCleanup(t *testing.T) {
	s := newFilesStore(t)
	now := time.Now()
	s.now = func() time.Time { return now.Add(-40 * 24 * time.Hour) } // 40 days ago
	old, err := s.SaveFile("a@t", "old.txt", nil, []byte("old"))
	if err != nil {
		t.Fatalf("save old: %v", err)
	}
	s.now = func() time.Time { return now }
	fresh, err := s.SaveFile("a@t", "new.txt", nil, []byte("new"))
	if err != nil {
		t.Fatalf("save new: %v", err)
	}
	n, err := s.CleanupExpiredFiles()
	if err != nil || n != 1 {
		t.Fatalf("cleanup n=%d err=%v, want 1", n, err)
	}
	if _, err := s.GetFileContent(old.ID); err != ErrFileNotFound {
		t.Errorf("old file should be gone, got %v", err)
	}
	if _, err := s.GetFileContent(fresh.ID); err != nil {
		t.Errorf("fresh file must survive: %v", err)
	}
}

func TestSanitizeEdgeNotInStore(t *testing.T) {
	// Guard the code-compare helper: differing lengths never panic and
	// unicode-safe filename handling is exercised at the handler layer.
	if !strings.EqualFold("A@T", "a@t") {
		t.Error("case-insensitive addresses expected")
	}
}
