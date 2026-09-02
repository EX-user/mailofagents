package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
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
	Wake(ctx context.Context, cfg *Config, sessionID, digest string) (string, int64, error)
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
// its history. timeBeat (duty_window_min due) and compactNotice
// (compact_notice_tokens due) ride at the top so the agent can run due
// scheduled tasks / persist memory before compaction happens.
func Digest(cfg *Config, mails []MailSummary, resumed bool, timeBeat, compactNotice string, stats MailStats, statsOK bool) string {
	var b strings.Builder
	if !resumed {
		tpl := strings.NewReplacer(
			"<address>", cfg.Address,
			"<password>", cfg.Password,
			"<serverURL>", cfg.Server,
			"<workdir>", cfg.Workdir,
		).Replace(cfg.Prompt)
		b.WriteString(tpl)
		if !strings.HasSuffix(tpl, "\n") {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "\n[工作目录] %s（你的项目/工区就在这里）\n", cfg.Workdir)
		fmt.Fprintf(&b, "\n[凭据] server=%s address=%s password=%s\n", cfg.Server, cfg.Address, cfg.Password)
		if statsOK {
			fmt.Fprintf(&b, "\n[收发统计] 邮箱累计收件 %d 封（当前未读 %d），累计发件 %d 封。\n",
				stats.InboxTotal, stats.UnreadCount, stats.SentTotal)
		}
		if len(mails) == 0 {
			// bootstrap wake: fresh session, empty inbox — the duty loop
			// starts the agent ahead of the first real mail. The onboarding
			// template above already covers credentials and memory-file
			// hygiene, so this note only adds what it doesn't say: why the
			// wake happened with nothing to process, that no reply is
			// needed, and who owns the duty loop. Mails arriving MID-turn
			// are explicitly in scope: the agent reads its inbox itself and
			// would otherwise mark them read without answering (the race
			// this note closes).
			b.WriteString("\n[值守初始化 / Bootstrap] 本次唤醒无待处理信件：完成环境自举后正常结束本轮即可，无需回信。" +
				"若本轮期间有新信到达，请照常处理并回复。" +
				"值守由外部 worker 负责，后续新信到达时你会被再次唤醒。\n")
		}
	}
	if compactNotice != "" {
		b.WriteString(compactNotice)
		b.WriteString("\n")
	}
	if timeBeat != "" {
		b.WriteString(timeBeat)
		b.WriteString("\n")
	}
	if len(mails) > 0 {
		fmt.Fprintf(&b, "收件箱当前状态：%d 未读（新→旧）\n", len(mails))
		show := mails
		if len(show) > 10 {
			fmt.Fprintf(&b, "（仅列最新 10 封，其余 %d 封更早未读未列出）\n", len(mails)-10)
			show = show[:10]
		}
		for i, m := range show {
			// Numbered, fielded, single-line entries: previews carry raw
			// body newlines (greeting + blank line + text), which used to
			// shred one record across many visual lines until records were
			// indistinguishable from each other. Previews clamp at 60
			// runes on top of the server-side 100.
			fmt.Fprintf(&b, "[未读 %d/%d]\n  发件人: %s\n  主题: %s（%s）\n  预览: %s\n",
				i+1, len(mails), m.From, oneLine(m.Subject),
				time.Unix(m.ReceivedAt, 0).Format("01-02 15:04"),
				oneLine(clampRunes(m.Preview, 60)))
		}
	}
	return b.String()
}

// oneLine collapses every whitespace run (newlines, tabs, multi-spaces) in
// s to a single space — digest entries must never span visual lines.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// clampRunes truncates s to at most n runes, appending an ellipsis when cut.
func clampRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
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

// lineTee feeds each stdout event's restrained summary into the account's
// status-board row (one line per account, redrawn in place). When the board
// is off (not a TTY), summaries fall back to plain log lines. The full
// stream still accumulates in the buffer for session-id extraction and
// error diagnostics.
type lineTee struct {
	tag      string
	buf      bytes.Buffer
	secret   string // account password: masked in anything shown on the board
	lastText string // most recent spoken text; its tail anchors the next step_start
	maxCtx   int64  // largest context-size report this wake (side requests report tiny usage and must not drag the readout down)
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
		if s := w.summarize(line); s != "" {
			board.Set(w.tag, "", SprintDetail(redact(s, w.secret)))
		}
	}
	return len(p), nil
}

// contextTokens reads the CONTEXT size out of a usage object — the input
// side (what the model actually saw this turn), per CLI field naming:
//
//	opencode: tokens.total (= input+cacheRead+output+reasoning, verified)
//	claude:   input_tokens + cache_read/creation_input_tokens
//	codex:    input_tokens (+cached_input_tokens)
//	pi:       input + cacheRead (+totalTokens)
//
// The LLM's input IS the context, so the last report is the authoritative
// session-size estimate; max() across the wake is what compact_notice
// compares against.
func contextTokens(o any) int64 {
	switch v := o.(type) {
	case map[string]any:
		if u, ok := v["usage"].(map[string]any); ok {
			if n := usageContext(u); n > 0 {
				return n
			}
		}
		if u, ok := v["tokens"].(map[string]any); ok {
			if n := usageContext(u); n > 0 {
				return n
			}
		}
		for _, vv := range v {
			if n := contextTokens(vv); n > 0 {
				return n
			}
		}
	case []any:
		for _, x := range v {
			if n := contextTokens(x); n > 0 {
				return n
			}
		}
	}
	return 0
}

func usageContext(u map[string]any) int64 {
	// context = input (uncached) + cache read + cache write; output and
	// reasoning excluded (they ride into next turn's input anyway).
	n := numOf(u, "input") + numOf(u, "input_tokens") + numOf(u, "cacheRead") +
		numOf(u, "cache_read_input_tokens") + numOf(u, "cached_input_tokens") +
		numOf(u, "cacheWrite") + numOf(u, "cache_creation_input_tokens")
	// opencode nests its cache counters one level down: tokens.cache.{read,
	// write} — the bulk of a cached session's context hides there.
	if c, ok := u["cache"].(map[string]any); ok {
		n += numOf(c, "read") + numOf(c, "write")
	}
	return n
}

// numOf reads a numeric field from a usage map (JSON numbers decode as
// float64).
func numOf(m map[string]any, k string) int64 {
	if f, ok := m[k].(float64); ok {
		return int64(f)
	}
	return 0
}

// summarize reduces one stdout line to a restrained board summary: the
// event type plus whatever is most informative in the payload — spoken text
// first, then a tool-call digest (name + partial params), then the context
// size when the event carries usage. Plain lines pass through truncated.
// Stateful via lastText: a step_start (generation phase, no payload) is
// anchored to what the model said last, so the silent stretch still reads
// as continuation.
func (w *lineTee) summarize(line []byte) string {
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
			var parts []string
			if s := findText(ev); s != "" {
				w.lastText = s
				parts = append(parts, s)
			} else if s := toolDigest(ev); s != "" {
				parts = append(parts, s)
			}
			if n := contextTokens(ev); n > 0 {
				// Track the wake's high-water mark: opencode interleaves
				// tiny side-request steps (title generation etc.) whose
				// usage would otherwise drag the readout down from the
				// main turn's real context size. The tee is per wake, so
				// the mark resets naturally (and after a real compaction).
				if n > w.maxCtx {
					w.maxCtx = n
				}
				board.SetCtx(w.tag, w.maxCtx)
				parts = append(parts, "ctx≈"+humanTokens(w.maxCtx))
			}
			if len(parts) == 0 {
				if t == "step_start" {
					if w.lastText != "" {
						return textTail(w.lastText, 60) + " · thinking…"
					}
					return "thinking…"
				}
				return t
			}
			return t + " | " + truncate(strings.Join(parts, " · "), 100)
		}
	}
	return "out | " + truncate(string(line), 100)
}

// textTail keeps the last n runes of s, prefixed with an ellipsis when cut.
func textTail(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n:])
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}

// toolDigest renders "name: params" for tool-call events. The name rides in
// tool/toolName/name; parameters are scalars from an inputs/args-style map
// or a bare command/path string; nested payloads (codex item.*, pi parts)
// are searched recursively.
func toolDigest(o any) string {
	switch v := o.(type) {
	case map[string]any:
		name := firstString(v, "tool", "toolName", "name")
		params := ""
		for _, k := range []string{"inputs", "input", "args", "arguments", "parameters"} {
			if m, ok := v[k].(map[string]any); ok && len(m) > 0 {
				params = mapDigest(m)
				break
			}
		}
		if params == "" {
			params = firstString(v, "command", "cmd", "file_path", "path", "pattern", "url", "query")
		}
		if params == "" {
			for _, vv := range v {
				if s := toolDigest(vv); s != "" {
					return s
				}
			}
			return ""
		}
		if name == "" {
			return params
		}
		return name + ": " + params
	case []any:
		for _, x := range v {
			if s := toolDigest(x); s != "" {
				return s
			}
		}
	}
	return ""
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// mapDigest renders up to three scalar key=value pairs of a params map,
// values clamped so the board row stays readable.
func mapDigest(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	n := 0
	for _, k := range keys {
		val := stringOf(m[k])
		if val == "" {
			if f, ok := m[k].(float64); ok {
				val = strconv.FormatFloat(f, 'g', -1, 64)
			} else {
				continue // nested payloads are too wide for one row
			}
		}
		if n > 0 {
			b.WriteString(", ")
		}
		b.WriteString(k + "=" + truncate(val, 40))
		n++
		if n == 3 {
			break
		}
	}
	return b.String()
}

// humanTokens renders a token count compactly: 32000 → 32k, 1250000 → 1.2M.
func humanTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	default:
		return strconv.FormatInt(n, 10)
	}
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

// Compacter is the optional compression capability: a CLI that exposes a
// headless summarize entry point can compact a session IN PLACE (full
// history serialized and summarized into summary messages, same session
// continues). Adapters without one don't implement it — the duty loop then
// keeps the session and leaves the reduction to the CLI's built-in
// in-place auto-compact (all four ship one).
type Compacter interface {
	CompactSession(ctx context.Context, cfg *Config, sessionID string) error
}

// runWake executes one CLI wake with the common hardening (workdir cwd,
// vendor env, tree-kill on timeout, WaitDelay) and returns stdout. tag is
// the account label for live event lines.
func runWake(ctx context.Context, cfg *Config, name string, args []string, stdinPayload, tag string) ([]byte, int64, error) {
	if err := ensureWorkdir(cfg.Workdir); err != nil {
		return nil, 0, fmt.Errorf("workdir: %v", err)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = cfg.Workdir
	cmd.Env = os.Environ()
	for k, v := range cfg.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	tee := &lineTee{tag: tag, secret: cfg.Password}
	cmd.Stdout = io.MultiWriter(&stdout, tee)
	cmd.Stderr = &stderr
	if stdinPayload != "" {
		cmd.Stdin = strings.NewReader(stdinPayload)
	}
	configureProcessGroup(cmd)
	// Graceful interruption: on timeout/cancel the process group gets a
	// SIGINT first — every CLI treats it as a user abort and exits on its
	// own. WaitDelay escalates to a hard kill only if it ignores the hint.
	cmd.WaitDelay = 15 * time.Second
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return interruptTree(cmd.Process.Pid)
	}

	if err := cmd.Run(); err != nil {
		// opencode >=1.18 writes its errors to the json stdout stream, not
		// stderr — both tails must ride along or the failure is blind. The
		// account password is redacted first: agents curl with -u user:pass
		// (the onboarding template teaches it) and a tool-call echo would
		// otherwise leak the credential into the terminal scrollback. The
		// raw stdout rides back too: a partial stream may still name the
		// session, which the caller salvages so a retry resumes the same
		// thread. The debug build widens the record further: exit code,
		// stdin mode, and an argv synopsis (long args — the digest — shown
		// by rune count + head) so argv-shape regressions are visible.
		exitInfo := ""
		var xe *exec.ExitError
		if errors.As(err, &xe) {
			exitInfo = fmt.Sprintf(" code=%d", xe.ExitCode())
		}
		stdinInfo := "off"
		if stdinPayload != "" {
			stdinInfo = fmt.Sprintf("%dB", len(stdinPayload))
		}
		argParts := make([]string, len(args))
		for i, a := range args {
			if n := utf8.RuneCountInString(a); n > 80 {
				argParts[i] = fmt.Sprintf("[%drunes]%q", n, string([]rune(a)[:60]))
			} else {
				argParts[i] = fmt.Sprintf("%q", a)
			}
		}
		stderrStr, stdoutStr := redact(stderr.String(), cfg.Password), redact(stdout.String(), cfg.Password)
		head := redact(string(stdout.Bytes()), cfg.Password)
		if r := []rune(head); len(r) > 300 {
			head = string(r[:300])
		}
		return stdout.Bytes(), 0, fmt.Errorf("%s wake: %v%s; stdin=%s; argv: %s; stderr(%dB): %s; stdout head: %s; stdout tail: %s",
			name, err, exitInfo, stdinInfo, strings.Join(argParts, " "),
			len(stderr.String()), truncate(stderrStr, 2000), head, truncate(stdoutStr, 800))
	}
	return stdout.Bytes(), 0, nil
}

// redact masks the watched account's password in CLI output that ends up on
// the status board or in error tails.
func redact(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "•••")
}

// ---- opencode: in-place session compaction ----
// POST /session/:id/summarize starts an async summarize turn (verified in
// 1.18.25 source: session/compaction.ts serializes the full history and
// writes summary messages back into the SAME session; overflow triggers it
// automatically too). The worker spins up a temporary `opencode serve` on a
// free local port, fires the request, and polls until the summary message
// lands. providerID/modelID come from cfg.Model ("provider/model").

func (opencodeAdapter) CompactSession(ctx context.Context, cfg *Config, sessionID string) error {
	prov, model := "", ""
	if cfg.Model != "" {
		if i := strings.Index(cfg.Model, "/"); i > 0 {
			prov, model = cfg.Model[:i], cfg.Model[i+1:]
		}
	}
	if prov == "" || model == "" {
		return fmt.Errorf("opencode compact needs cfg.Model as provider/model (got %q)", cfg.Model)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	env := os.Environ()
	for k, v := range cfg.Env {
		env = append(env, k+"="+v)
	}
	serve := exec.CommandContext(ctx, "opencode", "serve", "--port", strconv.Itoa(port), "--hostname", "127.0.0.1")
	serve.Dir = cfg.Workdir
	serve.Env = env
	configureProcessGroup(serve)
	if err := serve.Start(); err != nil {
		return fmt.Errorf("start opencode serve: %w", err)
	}
	defer func() {
		_ = killTree(serve.Process.Pid)
		_ = serve.Wait()
	}()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 15 * time.Second}
	post := func(path string, body map[string]any) error {
		b, _ := json.Marshal(body)
		resp, err := client.Post(base+path, "application/json", bytes.NewReader(b))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("%s -> %s", path, resp.Status)
		}
		return nil
	}

	// wait for the server to come up
	up := false
	for i := 0; i < 20; i++ {
		if err := post("/global/event", nil); err == nil {
			up = true
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	_ = up
	if err := post("/session/"+sessionID+"/summarize", map[string]any{"providerID": prov, "modelID": model}); err != nil {
		return fmt.Errorf("summarize: %w", err)
	}

	// poll until the compaction lands (summary assistant message appears)
	deadline := time.Now().Add(180 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
		resp, err := client.Get(base + "/session/" + sessionID + "/message")
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		var msgs []struct {
			Info struct {
				Role    string `json:"role"`
				Summary bool   `json:"summary"`
			} `json:"info"`
		}
		if json.Unmarshal(b, &msgs) == nil {
			for _, msg := range msgs {
				if msg.Info.Role == "assistant" && msg.Info.Summary {
					return nil // compaction landed
				}
			}
		}
	}
	return fmt.Errorf("compaction did not complete within 180s")
}

// ---- pi ----
// `pi -p --mode json` emits {"type":"session","id":...} as its first event;
// stdin carries the digest (double channel with the argv digest is fine).

type piAdapter struct{}

func (piAdapter) Wake(ctx context.Context, cfg *Config, sessionID, digest string) (string, int64, error) {
	sessionsDir := filepath.Join(cfg.Workdir, ".pi-sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		return "", 0, err
	}
	// pi's built-in compaction trigger is window-relative (settings.json
	// compaction.reserveTokens; contextWindow comes from model metadata) —
	// no absolute-limit knob to pin from here, so keep compact_notice_tokens
	// well below the model window: the notice round must land before the
	// built-in compaction fires.
	args := []string{"-p", "--mode", "json", "--session-dir", sessionsDir}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	if sessionID != "" {
		args = append(args, "--session", sessionID)
	}
	// Digest rides on STDIN only: npm-shim CLIs route argv through cmd.exe
	// on Windows, which truncates multi-line arguments to their first line
	// (field report: credentials never reached the session). pi reads the
	// prompt from stdin in print mode (phase 0: stdin ok).

	out, wakeTokens, err := runWake(ctx, cfg, "pi", args, digest, localPart(cfg.Address))
	if err != nil {
		// Salvage: a partial stream may still name the session.
		if id, perr := firstJSONField(out, "type", "session", "id"); perr == nil {
			return id, wakeTokens, err
		}
		return "", wakeTokens, err
	}
	id, err := firstJSONField(out, "type", "session", "id")
	if err != nil {
		return "", wakeTokens, fmt.Errorf("pi wake: %w", err)
	}
	return id, wakeTokens, nil
}

// ---- opencode ----
// `opencode run --format json` emits events whose top level carries
// "sessionID". stdin is FORBIDDEN on this CLI: on pipe EOF it disposes the
// instance and exits 0 with no output (structural fake success).

type opencodeAdapter struct{}

func (opencodeAdapter) Wake(ctx context.Context, cfg *Config, sessionID, digest string) (string, int64, error) {
	// full_perm (default on): --auto auto-approves every permission not
	// explicitly denied (official flag; 1.18 help). No workdir files written
	// — the workdir stays the agent's turf. The CLI surface has no compact
	// subcommand (session = list/delete only, verified 1.18.25); compaction
	// goes through the server API instead (CompactSession below: temporary
	// serve → POST /session/:id/summarize), with the built-in overflow
	// auto-compact as the safety net.
	args := []string{"run", "--format", "json"}
	if permOn(cfg) {
		args = append(args, "--auto")
	}
	// 1.18 can resolve its project from global state instead of the
	// inherited cwd on resume paths — pass --dir explicitly on top of
	// cmd.Dir (field report: agent saw the binary's dir as its workplace).
	args = append(args, "--dir", cfg.Workdir)
	if cfg.Model != "" {
		args = append(args, "-m", cfg.Model)
	}
	if sessionID != "" {
		args = append(args, "-s", sessionID)
	}
	// The digest rides as the positional message. opencode's stdin pipe is
	// structurally unusable (EOF ⇒ dispose ⇒ fake success), so argv is its
	// only channel — this append was dropped in the 0.2.3 stdin migration
	// (pi/claude/codex moved to stdin) and every opencode wake died with
	// "You must provide a message or a command" (field report 2026-09-02).
	args = append(args, digest)

	out, wakeTokens, err := runWake(ctx, cfg, "opencode", args, "", localPart(cfg.Address))
	if err != nil {
		// Salvage: a killed/failed run may still have announced the session.
		if id, perr := firstJSONField(out, "type", "", "sessionID"); perr == nil {
			return id, wakeTokens, err
		}
		return "", wakeTokens, err
	}
	id, err := firstJSONField(out, "type", "", "sessionID")
	if err != nil {
		return "", wakeTokens, fmt.Errorf("opencode wake: %w", err)
	}
	return id, wakeTokens, nil
}

// ---- claude code ----
// `claude -p --output-format json` prints ONE json object with top-level
// "session_id"; vendor credentials ride in cfg.Env as the three
// ANTHROPIC_* variables (DeepSeek official anthropic-compatible endpoint).

type claudeAdapter struct{}

func (claudeAdapter) Wake(ctx context.Context, cfg *Config, sessionID, digest string) (string, int64, error) {
	// Ordering guarantee for compact_notice_tokens (no worker-invocable
	// compaction entry here): cap the effective window above the notice
	// value so auto-compact fires only AFTER the notice round. Undocumented
	// but effective knob; a user-set value in cfg.Env wins — and if the user
	// drives the percentage knob instead, don't stack a window under it.
	if cfg.CompactNoticeTokens > 0 {
		_, hasWindow := cfg.Env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"]
		_, hasPct := cfg.Env["CLAUDE_AUTOCOMPACT_PCT_OVERRIDE"]
		if !hasWindow && !hasPct {
			c2 := *cfg
			c2.Env = make(map[string]string, len(cfg.Env)+1)
			for k, v := range cfg.Env {
				c2.Env[k] = v
			}
			c2.Env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] = strconv.FormatInt(noticeHeadroom(cfg), 10)
			cfg = &c2
		}
	}
	args := []string{"-p", "--output-format", "json"}
	// full_perm (default on): nobody is clicking approve in duty mode.
	// --dangerously-skip-permissions is blocked when running as root —
	// never run the worker as root anyway.
	if permOn(cfg) {
		args = append(args, "--dangerously-skip-permissions")
	}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	}
	// Digest rides on STDIN only: `claude -p` reads the prompt from stdin
	// when no positional prompt is given (phase 0: stdin ok). argv would go
	// through cmd.exe on Windows npm shims and truncate multi-line text.

	out, wakeTokens, err := runWake(ctx, cfg, "claude", args, digest, localPart(cfg.Address))
	if err != nil {
		// Salvage: a partial stream may still name the session.
		var sv struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal(bytes.TrimSpace(out), &sv) == nil && sv.SessionID != "" {
			return sv.SessionID, wakeTokens, err
		}
		return "", wakeTokens, err
	}
	var v struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &v); err != nil || v.SessionID == "" {
		return "", wakeTokens, fmt.Errorf("claude wake: no session_id in output (%d bytes)", len(out))
	}
	return v.SessionID, wakeTokens, nil
}

// ---- codex-cli ----
// `codex exec --json` emits JSONL whose first event is
// {"type":"thread.started","thread_id":...}. DeepSeek vendor config lives in
// ~/.codex/config.toml + models.json (responses wire API); the key comes via
// cfg.Env["DEEPSEEK_API_KEY"].

type codexAdapter struct{}

func (codexAdapter) Wake(ctx context.Context, cfg *Config, sessionID, digest string) (string, int64, error) {
	args := []string{"exec", "--json", "--skip-git-repo-check"}
	// Provider plumbing (base_url/env_key/wire_api) lives in
	// ~/.codex/config.toml — file-level, not automated; the model select is
	// overridable per wake via -c (value parsed as TOML, hence the quotes).
	if cfg.Model != "" {
		args = append(args, "-c", fmt.Sprintf("model=%q", cfg.Model))
	}
	// Ordering guarantee for compact_notice_tokens (no worker-invocable
	// compaction entry here): pin the built-in auto-compact threshold above
	// the notice value so the notice round always lands first. -c wins over
	// ~/.codex/config.toml, so this stays per-invocation (no global edits).
	if cfg.CompactNoticeTokens > 0 {
		args = append(args, "-c", fmt.Sprintf("model_auto_compact_token_limit=%d", noticeHeadroom(cfg)))
	}
	// full_perm (default on): the read-only default sandbox would cripple
	// duty work (nobody approves in exec mode either).
	if permOn(cfg) {
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}
	if sessionID != "" {
		args = append(args, "resume", sessionID)
	}
	// Digest rides on STDIN only: `codex exec` reads the prompt from stdin
	// when no positional prompt is given (phase 0: stdin native ok). argv
	// would go through cmd.exe on Windows npm shims and truncate
	// multi-line text — the field report that started this.

	out, wakeTokens, err := runWake(ctx, cfg, "codex", args, digest, localPart(cfg.Address))
	if err != nil {
		// Salvage: a partial stream may still name the thread.
		if id, perr := firstJSONField(out, "type", "thread.started", "thread_id"); perr == nil {
			return id, wakeTokens, err
		}
		return "", wakeTokens, err
	}
	id, err := firstJSONField(out, "type", "thread.started", "thread_id")
	if err != nil {
		return "", wakeTokens, fmt.Errorf("codex wake: %w", err)
	}
	return id, wakeTokens, nil
}

// permOn reports whether full tool permissions are requested (default on:
// nobody is clicking approve in duty mode). "full_perm": false opts out.
func permOn(cfg *Config) bool {
	return cfg.FullPerm != nil && *cfg.FullPerm
}

// noticeHeadroom is where CLIs without a worker-invocable compaction entry
// should fire their built-in auto-compact: compact_notice_tokens plus 25%
// (minimum +1), so the agent always sees the persist-memory notice round
// before the built-in compaction reduces the context.
func noticeHeadroom(cfg *Config) int64 {
	n := cfg.CompactNoticeTokens
	limit := n + n/4
	if limit <= n {
		limit = n + 1
	}
	return limit
}

// ensureWorkdirPermissions merges an "allow everything" permission block
// into the PROJECT-level opencode.json inside the workdir. Scope note
// (field decision 2026-09-01): --dir pins the session's project to the
// workdir, so this file is exactly what the session reads — the user's
// global opencode experience elsewhere is untouched. Existing content is
// preserved (generic-map merge, hand-tuned fields survive).
func ensureWorkdirPermissions(cfg *Config) error {
	path := filepath.Join(cfg.Workdir, "opencode.json")
	root := map[string]any{}
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		if err := json.Unmarshal(b, &root); err != nil {
			return fmt.Errorf("parse existing %s: %w", path, err)
		}
	}
	root["permission"] = map[string]any{
		"edit":     "allow",
		"bash":     "allow",
		"webfetch": "allow",
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".worker-tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
