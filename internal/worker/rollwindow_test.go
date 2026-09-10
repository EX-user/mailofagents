package worker

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// truncate must cut at rune boundaries: byte-exact slicing of CJK leaves
// an invalid continuation byte that renders as U+FFFD in board frames
// (caught by real-run frame capture 2026-09-10).
func TestTruncateRuneSafe(t *testing.T) {
	s := "工作记忆" + strings.Repeat("x", 120)
	got := truncate(s, 100)
	if !utf8.ValidString(got) {
		t.Fatalf("truncate produced invalid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("want ellipsis suffix, got %q", got)
	}
	if len(got) > 100+len("…") {
		t.Fatalf("over budget: %d bytes", len(got))
	}
	if got := truncate("short", 100); got != "short" {
		t.Fatalf("short string altered: %q", got)
	}
}

// clampEllipsis: over-width strings carry a visible trailing … within
// the column budget; exact-fit and short strings pass through untouched.
func TestClampEllipsis(t *testing.T) {
	if got := clampEllipsis("short", 100); got != "short" {
		t.Fatalf("short string altered: %q", got)
	}
	exact := strings.Repeat("a", 100)
	if got := clampEllipsis(exact, 100); got != exact {
		t.Fatalf("exact-fit string altered")
	}
	over := strings.Repeat("a", 120)
	got := clampEllipsis(over, 100)
	if !strings.HasSuffix(got, "…") || visualCols(got) != 100 {
		t.Fatalf("want 100 cols ending with …, got %q (%d cols)", got, visualCols(got))
	}
	cjk := strings.Repeat("汉", 60) // 120 cols
	got = clampEllipsis(cjk, 100)
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "…") || visualCols(got) > 100 || visualCols(got) < 98 {
		t.Fatalf("CJK clamp: %q (%d cols)", got, visualCols(got))
	}
}
