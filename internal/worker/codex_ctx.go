package worker

// Codex ctx readout — "measured anchor + tail estimate" (boss order
// 2026-10-15 15:38, research line same day):
//
// The authoritative anchor is the TokenUsage codex itself books into its
// session file (rollout JSONL): the last `token_count` event carries
// info.last_token_usage.total_tokens — the provider-reported active context
// (input incl. cached + output of the last response), exactly what codex's
// own /status shows. Entries recorded after that event (fresh user input,
// tool calls and outputs appended while the turn runs) are estimated locally
// at ~4 bytes per token (ceil), the same heuristic codex core uses between
// responses (codex-rs utils/string truncate.rs APPROX_BYTES_PER_TOKEN=4).
//
// Sources (openai/codex main, audited 2026-10-15):
//   - protocol/src/protocol.rs:2366 TokenUsage / :2401 TokenUsageInfo /
//     :2468 TokenCountEvent{info,rate_limits}
//   - rollout/src/list.rs:422 sessions dir layout
//     ~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl
//   - tui/src/token_usage.rs:32 tokens_in_context_window = last.total

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	codexTailBytesPerToken = 4
	codexScanLineCap       = 8 << 20 // tool outputs can be megabytes per line
)

// codexSessionsDir resolves the sessions root: CODEX_HOME from the account
// env wins (isolation per account), else the default ~/.codex.
func codexSessionsDir(env map[string]string) string {
	if h := env["CODEX_HOME"]; h != "" {
		return filepath.Join(h, "sessions")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "sessions")
}

// codexSessionCtx returns the anchored ctx readout for a codex thread:
// anchor (last token_count last_token_usage.total_tokens) + tail estimate
// for context-bearing items recorded after it. 0 when nothing usable is
// found (caller keeps the streamed-tee readout).
func codexSessionCtx(env map[string]string, threadID string) int64 {
	dir := codexSessionsDir(env)
	if dir == "" || threadID == "" {
		return 0
	}
	path := codexRolloutFile(dir, threadID)
	if path == "" {
		return 0
	}
	return codexRolloutCtx(path)
}

// codexRolloutFile finds the rollout file for a thread: newest match whose
// name carries the thread id (rollout-<ts>-<uuid>.jsonl).
func codexRolloutFile(dir, threadID string) string {
	var matches []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		if n := d.Name(); strings.HasSuffix(n, ".jsonl") && strings.Contains(n, threadID) {
			matches = append(matches, p)
		}
		return nil
	})
	if len(matches) == 0 {
		return ""
	}
	sort.Strings(matches) // name embeds the timestamp: last = newest
	return matches[len(matches)-1]
}

// codexRolloutCtx streams one rollout file and computes anchor + tail.
func codexRolloutCtx(path string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	anchor := int64(0)
	tailBytes := int64(0)
	seen := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), codexScanLineCap)
	for sc.Scan() {
		line := sc.Bytes()
		var rec struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		switch rec.Type {
		case "event_msg":
			// payload.type=="token_count" books the measured anchor: the
			// tail accumulated so far is superseded by real numbers.
			var ev struct {
				Type string `json:"type"`
				Info *struct {
					Last *struct {
						TotalTokens int64 `json:"total_tokens"`
					} `json:"last_token_usage"`
				} `json:"info"`
			}
			if json.Unmarshal(rec.Payload, &ev) == nil && ev.Type == "token_count" {
				if ev.Info != nil && ev.Info.Last != nil && ev.Info.Last.TotalTokens > 0 {
					anchor = ev.Info.Last.TotalTokens
					tailBytes = 0
					seen = true
				}
			}
		case "response_item":
			// Context-bearing transcript entry (user/assistant/tool items).
			// Serialized payload bytes / 4, ceil — same heuristic codex core
			// applies between responses. Overhead of the envelope is
			// excluded by using the raw payload only.
			tailBytes += int64(len(rec.Payload))
		}
	}
	if err := sc.Err(); err != nil || !seen {
		return 0
	}
	return anchor + (tailBytes+codexTailBytesPerToken-1)/codexTailBytesPerToken
}
