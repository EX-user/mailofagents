package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Real rollout shapes (openai/codex rollout JSONL, field names verified
// against codex-rs protocol.rs TokenCountEvent / rollout recorder).
func rolloutLine(typ string, payload string) string {
	return `{"timestamp":"2025-01-03T12:00:00.000Z","ordinal":7,"type":"` + typ + `","payload":` + payload + `}`
}

func tokenCountLine(total int64) string {
	payload := `{"type":"token_count","info":{"total_token_usage":{"input_tokens":` + itoa(total) +
		`,"cached_input_tokens":0,"output_tokens":10,"reasoning_output_tokens":0,"total_tokens":` + itoa(total+10) +
		`},"last_token_usage":{"input_tokens":` + itoa(total) +
		`,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":10,"reasoning_output_tokens":0,"total_tokens":` +
		itoa(total+10) + `},"model_context_window":273000},"rate_limits":null}`
	if !json.Valid([]byte(payload)) {
		panic("fixture payload invalid")
	}
	return rolloutLine("event_msg", payload)
}

func itoa(n int64) string {
	return strings.TrimSpace(strings.Replace(strings.Replace(
		strings.TrimLeft(fmtInt(n), "0"), "+", "", 1), " ", "", 1))
}

func fmtInt(n int64) string {
	// small helper without importing strconv twice in fixtures
	b := [24]byte{}
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
		if n == 0 {
			break
		}
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func TestCodexRolloutCtxAnchorPlusTail(t *testing.T) {
	dir := t.TempDir()
	// anchor: provider-measured 8890 input (incl. cached) + 10 output = 8900;
	// tail: two response_items — payload bytes counted, ceil(/4).
	tailA := `{"type":"function_call","name":"shell","arguments":"` + strings.Repeat("x", 100) + `"}`
	tailB := `{"type":"function_call_output","output":"` + strings.Repeat("y", 41) + `"}`
	lines := []string{
		rolloutLine("session_meta", `{"id":"uuid-1"}`),
		rolloutLine("response_item", `{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}`),
		tokenCountLine(8890),
		rolloutLine("response_item", tailA),
		rolloutLine("response_item", tailB),
		rolloutLine("event_msg", `{"type":"agent_message","message":"done"}`), // not context-bearing
	}
	p := filepath.Join(dir, "rollout-2025-01-03T12-00-00-uuid-1.jsonl")
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	want := int64(8900) + (int64(len(tailA)+len(tailB))+3)/4 // +3 = ceil rounding headroom
	got := codexRolloutCtx(p)
	if diff := got - 8900; diff != (int64(len(tailA)+len(tailB))+3)/4 {
		t.Fatalf("ctx = %d (tail %d), want anchor 8900 + ceil(%d/4) = %d",
			got, diff, len(tailA)+len(tailB), want)
	}
}

func TestCodexRolloutCtxAnchorOnlyAndMiss(t *testing.T) {
	dir := t.TempDir()
	sess := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sess, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(sess, "rollout-2025-01-03T12-00-00-uuid-2.jsonl")
	body := rolloutLine("session_meta", `{"id":"uuid-2"}`) + "\n" + tokenCountLine(8890) + "\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := codexRolloutCtx(p); got != 8900 {
		t.Fatalf("anchor-only = %d, want 8900", got)
	}
	if got := codexSessionCtx(map[string]string{"CODEX_HOME": dir}, "uuid-2"); got != 8900 {
		t.Fatalf("by-thread = %d, want 8900", got)
	}
	if got := codexSessionCtx(map[string]string{"CODEX_HOME": dir}, "absent"); got != 0 {
		t.Fatalf("miss = %d, want 0", got)
	}
	// nested YYYY/MM/DD layout still found by the walk
	nested := filepath.Join(dir, "sessions", "2025", "01", "03")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "rollout-2025-01-03T12-00-00-uuid-3.jsonl"),
		[]byte(rolloutLine("session_meta", `{"id":"uuid-3"}`)+"\n"+tokenCountLine(1000)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := codexSessionCtx(map[string]string{"CODEX_HOME": dir}, "uuid-3"); got != 1010 {
		t.Fatalf("nested = %d, want 1010", got)
	}
	if codexSessionsDir(map[string]string{}) == "" {
		t.Fatal("default sessions dir should resolve")
	}
}

func TestCodexRolloutCtxCorruptTailKeepsAnchor(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "rollout-2025-01-03T12-00-00-uuid-4.jsonl")
	// truncated last line (crash mid-write): the bad record is skipped, the
	// earlier anchor must survive.
	body := tokenCountLine(5000) + "\n" + `{"timestamp":"2025-01-03T12:00:01.000Z","type":"response_item","payl`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := codexRolloutCtx(p); got < 5010 {
		t.Fatalf("corrupt tail = %d, want >= anchor 5010", got)
	}
}
