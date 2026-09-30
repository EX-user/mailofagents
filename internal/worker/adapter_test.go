package worker

import (
	"strings"
	"testing"
)

// Construction assertions (0.2.4, weber-triage ②②): the exact argv/stdin
// shape each adapter builds, pinned per (adapter × digest shape). This is
// the regression class that killed every opencode wake in 0.2.3 — the
// stdin migration dropped opencode's argv append — and the 0.2.2 Windows
// field report (multi-line digest truncated by cmd.exe npm shims). Matrix:
// 4 adapters × 3 digest shapes (ASCII single-line / CJK / multi-line).
func TestAdapterInvocationShapes(t *testing.T) {
	digests := map[string]string{
		"ascii":     "unread 2: hello world",
		"cjk":       "未读 2：你好，世界——请处理",
		"multiline": "line one\nline two\nline three: 你好",
	}
	adapters := map[string]Adapter{
		"pi":       piAdapter{},
		"opencode": opencodeAdapter{},
		"claude":   claudeAdapter{},
		"codex":    codexAdapter{},
	}
	stdinAdapters := map[string]bool{"pi": true, "claude": true, "codex": true}

	base := &Config{
		Address:  "tester@example.com",
		Workdir:  t.TempDir(),
		FullPerm: func() *bool { b := true; return &b }(),
	}

	for aname, ad := range adapters {
		for dname, digest := range digests {
			name, args, stdin, _ := ad.Plan(base, "sess-123", digest)
			if name != aname {
				t.Errorf("%s/%s: Plan cli name = %q", aname, dname, name)
			}
			joined := strings.Join(args, "\x00")
			if stdinAdapters[aname] {
				// Digest must ride stdin ONLY — never in argv (cmd.exe
				// truncation class). Any argv element containing a
				// multi-line or CJK digest chunk is a regression.
				if stdin != digest {
					t.Errorf("%s/%s: stdin digest mismatch: got %d bytes, want %d", aname, dname, len(stdin), len(digest))
				}
				if strings.Contains(joined, digest) {
					t.Errorf("%s/%s: digest leaked into argv: %q", aname, dname, joined)
				}
			} else {
				// opencode: digest is the LAST argv element, stdin empty.
				if stdin != "" {
					t.Errorf("%s/%s: stdin must be empty (structural fake success), got %d bytes", aname, dname, len(stdin))
				}
				if len(args) == 0 || args[len(args)-1] != digest {
					t.Errorf("%s/%s: digest must be the final argv element, got tail %q", aname, dname, joined)
				}
			}
			// Session resume shape, every adapter.
			switch aname {
			case "pi":
				if !hasPair(args, "--session", "sess-123") {
					t.Errorf("%s/%s: missing --session sess-123 in %q", aname, dname, joined)
				}
			case "opencode":
				if !hasPair(args, "-s", "sess-123") {
					t.Errorf("%s/%s: missing -s sess-123 in %q", aname, dname, joined)
				}
			case "claude":
				if !hasPair(args, "--resume", "sess-123") {
					t.Errorf("%s/%s: missing --resume sess-123 in %q", aname, dname, joined)
				}
			case "codex":
				if !hasAdjacent(args, "resume", "sess-123") {
					t.Errorf("%s/%s: missing resume sess-123 in %q", aname, dname, joined)
				}
			}
		}
	}
}

// Claude's compact_notice ordering pin: when compact_notice_tokens is set
// and the user has no window/pct override, Plan derives the window env.
func TestClaudePlanWindowPin(t *testing.T) {
	cfg := &Config{Address: "t@e.com", Workdir: t.TempDir(), CompactNoticeTokens: 1000}
	_, _, _, cfg2 := claudeAdapter{}.Plan(cfg, "", "d")
	if cfg2 == cfg {
		t.Fatal("claude Plan must derive a config with the window override")
	}
	if cfg2.Env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] == "" {
		t.Error("claude Plan: CLAUDE_CODE_AUTO_COMPACT_WINDOW not pinned")
	}
	// User-set window wins: no override stacked.
	cfg3 := &Config{Address: "t@e.com", Workdir: t.TempDir(), CompactNoticeTokens: 1000,
		Env: map[string]string{"CLAUDE_CODE_AUTO_COMPACT_WINDOW": "9999"}}
	_, _, _, cfg4 := claudeAdapter{}.Plan(cfg3, "", "d")
	if cfg4.Env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] != "9999" {
		t.Error("claude Plan: user-set window must win")
	}
}

// Codex's notice ordering pin rides as -c model_auto_compact_token_limit.
func TestCodexPlanNoticePin(t *testing.T) {
	cfg := &Config{Address: "t@e.com", Workdir: t.TempDir(), CompactNoticeTokens: 1000}
	_, args, _, _ := codexAdapter{}.Plan(cfg, "", "d")
	found := false
	for _, a := range args {
		if strings.HasPrefix(a, "model_auto_compact_token_limit=") {
			found = true
		}
	}
	if !found {
		t.Error("codex Plan: model_auto_compact_token_limit pin missing")
	}
}

func hasPair(args []string, flag, val string) bool {
	for i, a := range args {
		if a == flag && i+1 < len(args) && args[i+1] == val {
			return true
		}
	}
	return false
}

func hasAdjacent(args []string, a, b string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == a && args[i+1] == b {
			return true
		}
	}
	return false
}

func TestDigestStripsControlBytes(t *testing.T) {
	// luowudi case (2026-09-04): a NUL riding the digest poisons opencode's
	// argv channel — fork/exec fails EINVAL before the CLI starts. Every
	// mail-sourced field must come out control-free.
	cfg := &Config{Prompt: "wake", Address: "a@x", Password: "p", Server: "https://s", Workdir: "/w"}
	mails := []MailSummary{
		{From: "peer@x", Subject: "PE/MZ 判定\x00 结果", Preview: "分片\x01补发中\x7f…", ReceivedAt: 1757000000},
		{From: "ot\x00her@x", Subject: "plain", Preview: "body", ReceivedAt: 1757000001},
	}
	d := Digest(cfg, mails, true, "", "", MailStats{}, false, 0)
	if strings.ContainsAny(d, "\x00\x01\x7f") {
		t.Fatalf("digest carries control bytes: %q", d)
	}
	if !strings.Contains(d, "PE/MZ 判定 结果") || !strings.Contains(d, "分片补发中…") {
		t.Fatalf("visible content damaged: %q", d)
	}
	if !strings.Contains(d, "other@x") {
		t.Fatalf("from field not sanitized: %q", d)
	}
}

func TestDigestLongUnreadReminder(t *testing.T) {
	// boss spec 2026-10-01: a session that never clears unread keeps
	// getting the same mails re-pushed. Past 3 rounds (exclusive, so 4+),
	// the digest must re-teach the two moves after the unread listing:
	// GET the body clears the unread, and /api/info self-describes the
	// connected system. Below the threshold the note must stay absent.
	cfg := &Config{Prompt: "wake", Address: "a@x", Password: "p", Server: "https://s.example", Workdir: "/w"}
	mails := []MailSummary{
		{ID: "01M", From: "peer@x", Subject: "stale", Preview: "p", ReceivedAt: 1757000000},
	}
	low := Digest(cfg, mails, true, "", "", MailStats{}, false, 3)
	if strings.Contains(low, "未销信提醒") {
		t.Fatalf("reminder must stay absent at 3 rounds: %q", low)
	}
	high := Digest(cfg, mails, true, "", "", MailStats{}, false, 4)
	if !strings.Contains(high, "未销信提醒") {
		t.Fatalf("reminder absent at 4 rounds: %q", high)
	}
	if !strings.Contains(high, "GET https://s.example/api/message?id=") {
		t.Fatalf("reminder must teach the GET-body endpoint: %q", high)
	}
	if !strings.Contains(high, "GET https://s.example/api/info") {
		t.Fatalf("reminder must point at the self-description endpoint: %q", high)
	}
	if strings.Contains(low, "未销信提醒") && false {
		t.Fatal("unreachable")
	}
	empty := Digest(cfg, nil, true, "", "", MailStats{}, false, 9)
	if strings.Contains(empty, "未销信提醒") {
		t.Fatalf("reminder must require an unread listing: %q", empty)
	}
}
