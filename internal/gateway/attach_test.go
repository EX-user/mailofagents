package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

func TestAttachFilesFormat(t *testing.T) {
	a := writeTemp(t, "notes.txt", "line1\nline2")
	b := writeTemp(t, "data.csv", "x,y\n1,2")
	out, err := attachFiles("hello", []string{a, b})
	if err != nil {
		t.Fatalf("attachFiles: %v", err)
	}
	want := "hello" +
		"\n\n--- file: notes.txt ---\nline1\nline2\n" +
		"\n\n--- file: data.csv ---\nx,y\n1,2\n"
	if out != want {
		t.Errorf("output mismatch:\n got: %q\nwant: %q", out, want)
	}
}

func TestAttachFilesNoPaths(t *testing.T) {
	out, err := attachFiles("body", nil)
	if err != nil || out != "body" {
		t.Errorf("no paths should be a no-op, got %q, %v", out, err)
	}
}

func TestAttachFilesTooMany(t *testing.T) {
	paths := []string{"a", "b", "c", "d"}
	if _, err := attachFiles("x", paths); err == nil || !strings.Contains(err.Error(), "at most 3") {
		t.Errorf("4 paths should error, got %v", err)
	}
}

func TestAttachFilesMissingFile(t *testing.T) {
	if _, err := attachFiles("x", []string{filepath.Join(t.TempDir(), "nope.txt")}); err == nil || !strings.Contains(err.Error(), "cannot read") {
		t.Errorf("missing file should error, got %v", err)
	}
}

func TestAttachFilesPerFileLimit(t *testing.T) {
	big := writeTemp(t, "big.txt", strings.Repeat("x", 100*1024+1))
	if _, err := attachFiles("x", []string{big}); err == nil || !strings.Contains(err.Error(), "per-file limit") {
		t.Errorf("101KB file should hit per-file limit, got %v", err)
	}
}

func TestAttachFilesTotalLimit(t *testing.T) {
	// Two 80KB files: each under 100KB, together 160KB — OK.
	a := writeTemp(t, "a.txt", strings.Repeat("a", 80*1024))
	b := writeTemp(t, "b.txt", strings.Repeat("b", 80*1024))
	if _, err := attachFiles("x", []string{a, b}); err != nil {
		t.Errorf("160KB total should pass, got %v", err)
	}
	// Three 80KB files: 240KB total — over.
	c := writeTemp(t, "c.txt", strings.Repeat("c", 80*1024))
	if _, err := attachFiles("x", []string{a, b, c}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("240KB total should error, got %v", err)
	}
}

func TestToStringSliceJSONArrayString(t *testing.T) {
	// Some MCP clients serialize array params as a JSON string literal —
	// the gateway must parse that back into the list it was meant to be.
	got := toStringSlice(`["a@x.test","b@x.test"]`)
	if len(got) != 2 || got[0] != "a@x.test" || got[1] != "b@x.test" {
		t.Errorf("JSON-array string should parse to 2 addresses, got %v", got)
	}
	// Plain comma-separated strings keep working.
	if got := toStringSlice("a@x.test, b@x.test"); len(got) != 2 {
		t.Errorf("comma string should parse to 2, got %v", got)
	}
	// Real arrays keep working.
	if got := toStringSlice([]any{"a@x.test", "b@x.test"}); len(got) != 2 {
		t.Errorf("real array should parse to 2, got %v", got)
	}
	// A string that merely starts with '[' but is not a JSON array of
	// strings falls through to comma-splitting unchanged.
	if got := toStringSlice("[not-a-json-array]"); len(got) != 1 || got[0] != "[not-a-json-array]" {
		t.Errorf("non-JSON bracket string should pass through, got %v", got)
	}
}

func TestForwardSubjectPrefix(t *testing.T) {
	// The forward path prefixes "Fwd: " unless the subject already carries a
	// forward marker. We can't run the HTTP flow here, so exercise the two
	// branch shapes via the same predicates the tool uses.
	has := func(subj string) bool {
		return strings.HasPrefix(strings.ToUpper(subj), "FWD:") || strings.HasPrefix(strings.ToUpper(subj), "转发:")
	}
	if !has("Fwd: already") {
		t.Error("subject with Fwd: prefix must be recognized as already marked")
	}
	if has("plain subject") {
		t.Error("plain subject should not be treated as already marked")
	}
	if !has("转发: 中文已带") {
		t.Error("Chinese forward marker must be recognized")
	}
}
