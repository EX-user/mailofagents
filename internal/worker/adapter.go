package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Adapter wakes the CLI coding agent: it builds the command line for one
// wake (new or resumed session), runs it inside the binding workdir with a
// hard timeout, and extracts the session id from the output.
//
// One config, four CLIs: switching agent = change cfg.CLI (+ its vendor
// block in cfg.Env); the duty loop, digest, resume chain, watchdog and
// alerting are shared (MVP acceptance line, alice 01M1DXSMS).
//
// phase 0 facts baked in here (field tests, 2026-09-01, see
// phase0_*_results.md):
//   - every wake MUST run with cwd == binding workdir (opencode cross-dir
//     resume fake-succeeds; the others are uniformed on cd for consistency).
//   - exit 0 with an empty/unparseable output counts as failure (opencode
//     fake-succeeds on stdin EOF; claude hangs on auth errors) — the common
//     gate below fails any wake whose session id can't be extracted.
//   - process trees are killed per-platform (kill_windows.go / kill_unix.go):
//     npm shim grandchildren hold the stdout pipe and would block Wait().
type Adapter interface {
	// Wake runs one non-interactive session turn. sessionID=="" means new
	// session. It returns the (possibly new) session id.
	Wake(ctx context.Context, cfg *Config, sessionID, digest string) (string, error)
}

func pickAdapter(id string) Adapter {
	switch id {
	case "pi":
		return piAdapter{}
	case "opencode":
		return opencodeAdapter{}
	case "claude":
		return claudeAdapter{}
	case "codex":
		return codexAdapter{}
	default:
		return piAdapter{}
	}
}

// Digest renders the wake payload. Superior feedback: keep the unread
// injection light — short mechanical text, no full JSON. The fresh-session
// wake carries the project's approved v4 post-registration prompt template
// (default cfg.Prompt; placeholders substituted here, same contract as the
// panel's buildAgentPrompt) so the session is self-sufficient via the same
// onboarding every registered agent receives. A resumed session gets the
// unread snapshot ONLY — role, credentials and API knowledge are already in
// its history. timeBeat (duty_window_min due) rides at the top so the agent
// can run due scheduled tasks even when the inbox is empty.
func Digest(cfg *Config, mails []MailSummary, resumed bool, timeBeat string) string {
	var b strings.Builder
	if !resumed {
		tpl := strings.NewReplacer(
			"<address>", cfg.Address,
			"<password>", cfg.Password,
			"<serverURL>", cfg.Server,
		).Replace(cfg.Prompt)
		b.WriteString(tpl)
		if !strings.HasSuffix(tpl, "\n") {
			b.WriteString("\n")
		}
	}
	if timeBeat != "" {
		b.WriteString(timeBeat)
		b.WriteString("\n")
	}
	if len(mails) > 0 {
		fmt.Fprintf(&b, "收件箱当前状态：%d 未读\n", len(mails))
		for _, m := range mails {
			fmt.Fprintf(&b, "%s from %s: %s\n", m.Subject, m.From, m.Preview)
		}
	}
	if !resumed {
		fmt.Fprintf(&b, "\n[凭据] server=%s address=%s password=%s\n", cfg.Server, cfg.Address, cfg.Password)
	}
	return b.String()
}

// ensureWorkdir materializes the binding workdir conservatively (superior
// directive): missing path with an EXISTING parent => create just that one
// level; missing parent chain => error, never a silent mkdir -p.
func ensureWorkdir(path string) error {
	if st, err := os.Stat(path); err == nil {
		if st.IsDir() {
			return nil
		}
		return fmt.Errorf("workdir %s exists but is not a directory", path)
	}
	parent := filepath.Dir(path)
	if st, err := os.Stat(parent); err != nil || !st.IsDir() {
		return fmt.Errorf("workdir %s: parent %s does not exist (create it first)", path, parent)
	}
	return os.Mkdir(path, 0o755)
}

// lineTee tees the CLI's stdout into the worker log as one restrained
// summary line per event, while the full stream keeps flowing into the
// buffer for session-id extraction and error diagnostics.
type lineTee struct {
	tag  string
	name string
	buf  bytes.Buffer
}

func (w *lineTee) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		b := w.buf.Bytes()
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			break
		}
		line := append([]byte(nil), b[:i]...)
		w.buf.Next(i + 1)
		if s := eventSummary(line); s != "" {
			log.Printf("[%s][%s] %s", w.tag, w.name, s)
		}
	}
	return len(p), nil
}

// eventSummary reduces one stdout line to a restrained summary: for JSON
// events the type plus the first embedded text; plain lines are truncated.
func eventSummary(line []byte) string {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return ""
	}
	if line[0] == '{' {
		var ev map[string]any
		if json.Unmarshal(line, &ev) == nil {
			t, _ := ev["type"].(string)
			if t == "" {
				if id, ok := ev["session_id"].(string); ok {
					return "session_id=" + truncate(id, 20) + " result=" + truncate(stringOf(ev["result"]), 80)
				}
				return "json(" + truncate(string(line), 60) + ")"
			}
			if s := findText(ev); s != "" {
				return t + " | " + truncate(s, 100)
			}
			return t
		}
	}
	return "out | " + truncate(string(line), 100)
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}

// findText digs out the first non-empty "text" field (events nest it under
// part/item/message differently per CLI).
func findText(o any) string {
	switch v := o.(type) {
	case map[string]any:
		if s, ok := v["text"].(string); ok && s != "" {
			return s
		}
		for _, vv := range v {
			if s := findText(vv); s != "" {
				return s
			}
		}
	case []any:
		for _, x := range v {
			if s := findText(x); s != "" {
				return s
			}
		}
	}
	return ""
}

// runWake executes one CLI wake with the common hardening (workdir cwd,
// vendor env, tree-kill on timeout, WaitDelay) and returns stdout. tag is
// the account label for live event lines.
func runWake(ctx context.Context, cfg *Config, name string, args []string, stdinPayload, tag string) ([]byte, error) {
	if err := ensureWorkdir(cfg.Workdir); err != nil {
		return nil, fmt.Errorf("workdir: %v", err)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = cfg.Workdir
	cmd.Env = os.Environ()
	for k, v := range cfg.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	tee := &lineTee{tag: tag, name: name}
	cmd.Stdout = io.MultiWriter(&stdout, tee)
	cmd.Stderr = &stderr
	if stdinPayload != "" {
		cmd.Stdin = strings.NewReader(stdinPayload)
	}
	configureProcessGroup(cmd)
	cmd.WaitDelay = 10 * time.Second
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return killTree(cmd.Process.Pid)
	}

	if err := cmd.Run(); err != nil {
		// opencode >=1.18 writes its errors to the json stdout stream, not
		// stderr — both tails must ride along or the failure is blind.
		return nil, fmt.Errorf("%s wake: %v; stderr: %s; stdout tail: %s",
			name, err, truncate(stderr.String(), 400), truncate(stdout.String(), 600))
	}
	return stdout.Bytes(), nil
}

// ---- pi ----
// `pi -p --mode json` emits {"type":"session","id":...} as its first event;
// stdin carries the digest (double channel with the argv digest is fine).

type piAdapter struct{}

func (piAdapter) Wake(ctx context.Context, cfg *Config, sessionID, digest string) (string, error) {
	sessionsDir := filepath.Join(cfg.Workdir, ".pi-sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		return "", err
	}
	args := []string{"-p", "--mode", "json", "--session-dir", sessionsDir}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	if sessionID != "" {
		args = append(args, "--session", sessionID)
	}
	args = append(args, digest)

	out, err := runWake(ctx, cfg, "pi", args, digest, localPart(cfg.Address))
	if err != nil {
		return "", err
	}
	id, err := firstJSONField(out, "type", "session", "id")
	if err != nil {
		return "", fmt.Errorf("pi wake: %w", err)
	}
	return id, nil
}

// ---- opencode ----
// `opencode run --format json` emits events whose top level carries
// "sessionID". stdin is FORBIDDEN on this CLI: on pipe EOF it disposes the
// instance and exits 0 with no output (structural fake success).

type opencodeAdapter struct{}

func (opencodeAdapter) Wake(ctx context.Context, cfg *Config, sessionID, digest string) (string, error) {
	// No -m: opencode falls back to the MOST RECENTLY USED model (sticky,
	// field-verified 2026-09-01) — a human using the TUI silently changes
	// the worker's next wake. Pin cfg.Model for deterministic duty.
	args := []string{"run", "--format", "json"}
	if cfg.Model != "" {
		args = append(args, "-m", cfg.Model)
	}
	if sessionID != "" {
		args = append(args, "-s", sessionID)
	}
	args = append(args, digest)

	out, err := runWake(ctx, cfg, "opencode", args, "", localPart(cfg.Address))
	if err != nil {
		return "", err
	}
	id, err := firstJSONField(out, "type", "", "sessionID")
	if err != nil {
		return "", fmt.Errorf("opencode wake: %w", err)
	}
	return id, nil
}

// ---- claude code ----
// `claude -p --output-format json` prints ONE json object with top-level
// "session_id"; vendor credentials ride in cfg.Env as the three
// ANTHROPIC_* variables (DeepSeek official anthropic-compatible endpoint).

type claudeAdapter struct{}

func (claudeAdapter) Wake(ctx context.Context, cfg *Config, sessionID, digest string) (string, error) {
	args := []string{"-p", "--output-format", "json"}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	}
	args = append(args, digest)

	out, err := runWake(ctx, cfg, "claude", args, "", localPart(cfg.Address))
	if err != nil {
		return "", err
	}
	var v struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &v); err != nil || v.SessionID == "" {
		return "", fmt.Errorf("claude wake: no session_id in output (%d bytes)", len(out))
	}
	return v.SessionID, nil
}

// ---- codex-cli ----
// `codex exec --json` emits JSONL whose first event is
// {"type":"thread.started","thread_id":...}. DeepSeek vendor config lives in
// ~/.codex/config.toml + models.json (responses wire API); the key comes via
// cfg.Env["DEEPSEEK_API_KEY"].

type codexAdapter struct{}

func (codexAdapter) Wake(ctx context.Context, cfg *Config, sessionID, digest string) (string, error) {
	args := []string{"exec", "--json", "--skip-git-repo-check"}
	// Provider plumbing (base_url/env_key/wire_api) lives in
	// ~/.codex/config.toml — file-level, not automated; the model select is
	// overridable per wake via -c (value parsed as TOML, hence the quotes).
	if cfg.Model != "" {
		args = append(args, "-c", fmt.Sprintf("model=%q", cfg.Model))
	}
	if sessionID != "" {
		args = append(args, "resume", sessionID)
	}
	args = append(args, digest)

	out, err := runWake(ctx, cfg, "codex", args, "", localPart(cfg.Address))
	if err != nil {
		return "", err
	}
	id, err := firstJSONField(out, "type", "thread.started", "thread_id")
	if err != nil {
		return "", fmt.Errorf("codex wake: %w", err)
	}
	return id, nil
}

// firstJSONField scans newline-delimited JSON events for the first object
// whose "type" matches wantType (empty = any) and returns the given id
// field. An empty result is an error: exit 0 without a session event is a
// fake success (opencode), and a wake without a session id can't be resumed.
func firstJSONField(out []byte, typeKey, wantType, idField string) (string, error) {
	for _, line := range bytes.Split(out, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if wantType != "" && ev[typeKey] != wantType {
			continue
		}
		if id, ok := ev[idField].(string); ok && id != "" {
			return id, nil
		}
	}
	return "", fmt.Errorf("no %s in output (%d bytes)", idField, len(out))
}
