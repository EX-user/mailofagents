package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Duty is the v4 watch loop: poll the unread list; when non-empty, wake the
// CLI session with a mechanical digest. No cursor — the unread list IS the
// work queue; the agent clears unread server-side as it answers.
type Duty struct {
	cfg     *Config
	mail    *MailClient
	adapter Adapter
	fresh   bool // -fresh: ignore the stored session binding and clean worker artifacts

	mu             sync.Mutex
	sessionID      string // current bound session ("" = start a new one next wake)
	failStreak     int
	lastAlert      time.Time
	lastBeat       time.Time     // duty-window anchor: process start or last time-beat wake
	startedAt      time.Time     // process start, for the uptime stamp in heartbeats
	urgentHit      atomic.Bool   // set when an urgent mail interrupted the current wake
	urgentCh       chan struct{} // capacity-1: fires an immediate re-check after an urgent interrupt
	superiors      []string      // fallback escalation addresses: declared superiors via /api/subs (refreshed every 10 min)
	compactPending bool          // notice due: next wake carries the persist-memory notice; compact in place after it
	lastFailShort  string        // last failure reason shown (failure-line throttle)
	lastFailAt     time.Time     // when that reason was last logged
}

// cliWakeLocks serializes wakes per CLI id within this worker process (see
// the comment at the Wake call site in checkOnce).
var cliWakeLocks sync.Map // cli id -> *sync.Mutex

func NewDuty(cfg *Config, fresh bool) *Duty {
	return &Duty{
		cfg:      cfg,
		mail:     NewMailClient(cfg.Server, cfg.Address, cfg.Password),
		adapter:  pickAdapter(cfg.CLI),
		fresh:    fresh,
		urgentCh: make(chan struct{}, 1),
	}
}

// logf prefixes every duty log line with the account local-part so
// parallel account loops stay distinguishable in one stream; it routes
// through the status board (erase → line → redraw when on a TTY).
func (d *Duty) logf(format string, args ...any) {
	board.Logf(localPart(d.cfg.Address), format, args...)
}

// verboseEnabled reports whether pre-flight wake lines (unread counts,
// time beats — both already visible on the status board) should also hit
// the log. WORKER_VERBOSE=1 turns them on; quiet-by-default keeps the
// terminal scrollback to roughly one line per completed wake.
func verboseEnabled() bool {
	return os.Getenv("WORKER_VERBOSE") == "1"
}

// failLog renders a failure as ONE throttled log line: a reason repeating
// since the previous failure line (or within a 30-minute episode of it)
// is archived to the error file but not re-logged, so even a flapping
// account leaves the scrollback flat. WORKER_VERBOSE=1 echoes the full
// detail every time.
func (d *Duty) failLog(short string, detail error) {
	d.mu.Lock()
	newEpisode := short != d.lastFailShort || time.Since(d.lastFailAt) > 30*time.Minute
	d.lastFailShort = short
	d.lastFailAt = time.Now()
	d.mu.Unlock()
	if verboseEnabled() {
		d.logf("wake failed: %s | detail: %v", short, detail)
		return
	}
	if newEpisode {
		d.logf("wake failed: %s — repeats archived to errors-%s.log", short, localPart(d.cfg.Address))
	}
}

// shortErr compresses a wake failure into one board-friendly line: the
// classified reason instead of the multi-hundred-byte stderr/stdout tails
// (which errorLog archives to the per-account error file).
func shortErr(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "database is locked"):
		return "opencode db lock (retry queued)"
	case strings.Contains(s, "certificate"), strings.Contains(s, "stream disconnected"),
		strings.Contains(s, "Reconnecting"), strings.Contains(s, "connect"):
		return "network/tls unreachable"
	case strings.Contains(s, "429"), strings.Contains(s, "usage"),
		strings.Contains(s, "quota"), strings.Contains(s, "限额"),
		strings.Contains(s, "使用上限"):
		return "provider quota/429"
	case strings.Contains(s, "signal: killed"):
		return "timeout kill"
	}
	// Generic exit: drop the embedded tails, keep the head.
	if i := strings.Index(s, "; stderr:"); i > 0 {
		s = s[:i]
	}
	return s
}

// errorLog appends a wake failure's FULL detail (tails included) to a
// per-account error file next to the state file, rotated at ~256KB. The
// terminal only ever shows the shortErr line.
func (d *Duty) errorLog(err error) {
	path := filepath.Join(filepath.Dir(d.cfg.StateFile), "errors-"+localPart(d.cfg.Address)+".log")
	if fi, err := os.Stat(path); err == nil && fi.Size() > 256*1024 {
		_ = os.Rename(path, path+".old")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "=== %s wake failure ===\n%v\n\n", time.Now().Format("2006-01-02 15:04:05"), err)
}

// contactAddrs resolves the escalation address set: explicit config wins;
// otherwise the account's declared superiors (refreshed every 10 min).
func (d *Duty) contactAddrs() []string {
	if len(d.cfg.Emergency.Addresses) > 0 {
		return d.cfg.Emergency.Addresses
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.superiors
}

// refreshContacts pulls the account's declared superiors once at startup
// and then every 10 minutes — subs edges can change while on duty.
func (d *Duty) refreshContacts(ctx context.Context) {
	for {
		if sups, err := d.mail.Subs(); err == nil {
			d.mu.Lock()
			d.superiors = sups
			d.mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Minute):
		}
	}
}

// statePath returns the session binding store — next to the config by
// default (the workdir is the agent's turf), overridable via "state_file".
// Legacy location (workdir/.worker-state.json) is migrated once, silently.
func (d *Duty) statePath() string { return d.cfg.StateFile }

func (d *Duty) loadState() {
	if d.fresh {
		return
	}
	// one-time migration from the pre-split location
	legacy := filepath.Join(d.cfg.Workdir, ".worker-state.json")
	if _, err := os.Stat(d.cfg.StateFile); os.IsNotExist(err) {
		if b, lerr := os.ReadFile(legacy); lerr == nil {
			if werr := os.WriteFile(d.cfg.StateFile, b, 0o600); werr == nil {
				_ = os.Remove(legacy)
				d.logf("migrated session state %s -> %s", legacy, d.cfg.StateFile)
			}
		}
	}
	b, err := os.ReadFile(d.statePath())
	if err != nil {
		return
	}
	var s struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(b, &s) == nil {
		d.sessionID = s.SessionID
	}
}

func (d *Duty) saveState() {
	b, _ := json.Marshal(map[string]string{"session_id": d.sessionID})
	tmp := d.statePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, d.statePath()) // atomic swap
}

// Run polls until ctx is cancelled (SIGTERM → graceful stop).
func (d *Duty) Run(ctx context.Context) {
	tag := localPart(d.cfg.Address)
	board.AddRow(tag, time.Now(), d.cfg.ContextWindow, d.cfg.CompactNoticeTokens)
	// Binding workdir: create the last level on startup when missing
	// (parent must exist — no silent mkdir -p); log-only on failure so the
	// loop keeps polling (each wake will surface the error too).
	if err := ensureWorkdir(d.cfg.Workdir); err != nil {
		d.logf("workdir: %v", err)
	}
	if d.fresh {
		d.cleanWorkdir()
	} else {
		d.loadState()
	}
	d.logf("duty start: server=%s cli=%s workdir=%s fresh=%v duty_window=%dm session=%q",
		d.cfg.Server, d.cfg.CLI, d.cfg.Workdir, d.fresh, d.cfg.DutyWindowMin, d.sessionID)
	d.lastBeat = time.Now()
	d.startedAt = time.Now()
	go d.refreshContacts(ctx)

	t := time.NewTicker(time.Duration(d.cfg.PollIntervalSec) * time.Second)
	defer t.Stop()
	for {
		d.safeCheckOnce(ctx)
		select {
		case <-ctx.Done():
			d.mu.Lock()
			sess := d.sessionID
			d.mu.Unlock()
			// Superior request: on shutdown, hand back every address+session
			// pair — the session id is the fastest way back into a live
			// scene (manual resume / support triage).
			d.logf("duty stop: %v address=%s session=%q", ctx.Err(), d.cfg.Address, sess)
			// Graceful-exit self-mail: an unread self-addressed letter
			// guarantees the next start wakes the CLI (a bound session with
			// an empty inbox would otherwise just sit waiting), so the agent
			// learns it was interrupted and when. Best-effort: shutdown
			// proceeds regardless of the send result.
			if sess != "" {
				stamp := time.Now().Format("060102150405")
				if err := d.mail.SendMail(d.cfg.Address,
					"[worker] 会话被掐断 "+stamp,
					stamp+" 外部值守进程掐断了会话。"); err != nil {
					d.logf("shutdown self-mail failed: %v", err)
				} else {
					d.logf("shutdown self-mail sent (guarantees a wake on next start)")
				}
			}
			return
		case <-t.C:
		case <-d.urgentCh: // urgent interrupt landed: re-check at once
		}
	}
}

// safeCheckOnce contains a panicking round to this account: multi-account
// workers share one process, and an unrecovered panic in one duty goroutine
// would take every account down with it.
func (d *Duty) safeCheckOnce(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			d.logf("panic recovered in checkOnce: %v", r)
			d.noteFailure(fmt.Sprintf("panic: %v", r))
		}
	}()
	d.checkOnce(ctx)
}

func (d *Duty) urgentNow() {
	select {
	case d.urgentCh <- struct{}{}:
	default:
	}
}

// cleanWorkdir implements -fresh: drop the stored session binding AND
// clear the workdir contents (the workdir is the agent's working area —
// -fresh means a brand-new start, so leftover work files go too; the
// directory itself is kept). With -switch_address only the selected
// account's workdir is cleared; full runs clear each account's own
// workdir. CLI-internal session stores (e.g. opencode's global
// ~/.local/share/opencode) are not touched — clearing the binding already
// guarantees a fresh session.
func (d *Duty) cleanWorkdir() {
	d.logf("fresh: dropping session binding and clearing workdir contents")
	_ = os.Remove(d.statePath())
	_ = os.Remove(filepath.Join(d.cfg.Workdir, ".worker-state.json")) // legacy location
	entries, err := os.ReadDir(d.cfg.Workdir)
	if err != nil {
		d.logf("fresh: read workdir: %v", err)
		d.sessionID = ""
		return
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(d.cfg.Workdir, e.Name())); err != nil {
			d.logf("fresh: remove %s: %v", e.Name(), err)
		} else {
			d.logf("fresh: removed %s", e.Name())
		}
	}
	d.sessionID = ""
}

// dutyDue reports whether the configured max continuous duty stretch has
// elapsed since the last time-beat (or process start). 0/absent = unlimited.
func (d *Duty) dutyDue() bool {
	if d.cfg.DutyWindowMin <= 0 {
		return false
	}
	return time.Since(d.lastBeat) >= time.Duration(d.cfg.DutyWindowMin)*time.Minute
}

// urgentArrived reports whether a mail FROM alert.to whose subject/preview
// contains the urgent phrase is sitting unread — the interrupt signal.
func (d *Duty) urgentArrived() bool {
	if d.cfg.Emergency.UrgentPhrase == "" {
		return false
	}
	unread, err := d.mail.UnreadInbox(50)
	if err != nil {
		return false
	}
	addrs := d.contactAddrs()
	for _, m := range unread {
		fromMatch := false
		for _, a := range addrs {
			if m.From == a {
				fromMatch = true
				break
			}
		}
		if !fromMatch {
			continue
		}
		if strings.Contains(m.Subject, d.cfg.Emergency.UrgentPhrase) || strings.Contains(m.Preview, d.cfg.Emergency.UrgentPhrase) {
			return true
		}
	}
	return false
}

// watchUrgent polls for the urgent phrase while a wake is in flight; on a
// hit it flips the flag and cancels the wake (tree kill), so the loop can
// immediately re-wake with the urgent mail first in the digest.
func (d *Duty) watchUrgent(wakeCtx context.Context, cancel context.CancelFunc, done func()) {
	defer done()
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-wakeCtx.Done():
			return
		case <-t.C:
			if d.urgentArrived() {
				d.urgentHit.Store(true)
				cancel()
				return
			}
		}
	}
}

func (d *Duty) checkOnce(ctx context.Context) {
	tag := localPart(d.cfg.Address)
	unread, err := d.mail.UnreadInbox(50)
	if err != nil {
		short := "poll: " + shortErr(err)
		d.failLog(short, err)
		board.Set(tag, "waiting", "poll failed: "+shortErr(err))
		d.noteFailure(short)
		return
	}
	dutyDue := d.dutyDue()
	// Heartbeat: instead of a log line, the account's board row shows the
	// live queue size and uptime (plain log line when the board is off).
	dueMark := ""
	if dutyDue {
		dueMark = " [duty window due]"
	}
	board.Set(tag, "waiting", fmt.Sprintf("%d unread%s", len(unread), dueMark))
	// Empty-inbox wake suppressors, with two exceptions: compact_pending
	// forces a round (persist memory before compaction), and an empty
	// session id forces the bootstrap round — onboarding with no mail yet,
	// so the agent orients itself and builds its memory file ahead of the
	// first real mail.
	if len(unread) == 0 && !dutyDue && !d.compactPending && d.sessionID != "" {
		d.failStreak = 0
		return
	}
	if len(unread) > 0 {
		// The board row already shows the unread count and the waiting→
		// working transition, so this pre-flight line is scrollback noise
		// during normal duty; WORKER_VERBOSE=1 restores it.
		if verboseEnabled() {
			d.logf("wake: %d unread (session %q)", len(unread), d.sessionID)
		}
	} else if d.sessionID == "" {
		d.logf("wake: bootstrap (empty inbox, fresh session)")
	} else if verboseEnabled() {
		d.logf("wake: time beat (session %q)", d.sessionID)
	}
	board.Set(tag, "working", "digest sent, model is on it…")

	var timeBeat string
	if dutyDue {
		timeBeat = fmt.Sprintf("[报时] 值守已连续运行 %s，当前时间 %s。若你有到点的定时任务请执行；没有则本条无需回信，知悉即可。",
			time.Since(d.lastBeat).Round(time.Second), time.Now().Format("2006-01-02 15:04:05 -0700"))
		d.lastBeat = time.Now()
	}

	// compact notice (bilingual, per superior ruling on memory hygiene):
	// delivered on the round AFTER the token threshold was crossed;
	// compaction (in place or built-in) happens when this round completes.
	var compactNotice string
	if d.compactPending {
		compactNotice = ("[压缩预告 / Session compaction notice] 本次唤醒结束后，会话将被压缩。" +
			"The session will be compacted right after this wake. 请先把需要延续的信息" +
			"写入并更新你的记忆文件（memory file in the workdir），再正常结束本轮。\n" +
			"Please persist or update your memory file BEFORE finishing this wake.")
	}

	wakeCtx, cancel := context.WithTimeout(ctx, time.Duration(d.cfg.TimeoutSec)*time.Second)
	defer cancel()
	resumed := d.sessionID != ""
	// volume snapshot for the fresh-session digest (two light calls; a
	// failure just omits the stats block)
	stats, statsErr := d.mail.Stats()
	// The urgent watcher lives only as long as the CLI PROCESS runs and is
	// joined right after Wake returns. Deferring the join to the function
	// end deadlocked the duty loop until the wake timeout: the watcher
	// exits only when wakeCtx is done, and the deferred cancel ran AFTER
	// the join — with long timeouts the loop froze for the whole budget
	// after every successful wake.
	var urgentDone chan struct{}
	if d.cfg.Emergency.UrgentPhrase != "" {
		urgentDone = make(chan struct{})
		go d.watchUrgent(wakeCtx, cancel, func() { close(urgentDone) })
	}
	// Serialize wakes per CLI within this worker process: opencode keeps a
	// single global SQLite session store per user, so two concurrent
	// spawns collide on "database is locked" at init (multi-account
	// same-poll wake). Cross-process collisions are out of scope — run one
	// worker per host, or give each account its own XDG data dir.
	muAny, _ := cliWakeLocks.LoadOrStore(d.cfg.CLI, &sync.Mutex{})
	wakeMu := muAny.(*sync.Mutex)
	wakeMu.Lock()
	newID, wakeTokens, err := func() (string, int64, error) {
		defer wakeMu.Unlock()
		return d.adapter.Wake(wakeCtx, d.cfg, d.sessionID, Digest(d.cfg, unread, resumed, timeBeat, compactNotice, stats, statsErr == nil))
	}()
	if urgentDone != nil {
		cancel() // wake over: the watcher exits via its wakeCtx select
		<-urgentDone
	}
	if ctx.Err() != nil {
		return // shutting down; do not count as failure
	}
	if err != nil {
		// Salvage: a partial stream may still have announced the session —
		// bind it silently so the retry resumes the same thread instead of
		// starting over with a blank context.
		if newID != "" {
			d.mu.Lock()
			salvaged := d.sessionID != newID
			d.sessionID = newID
			d.saveState()
			d.mu.Unlock()
			_ = salvaged
		}
		if d.urgentHit.Load() {
			// urgent interrupt: interrupt landed; re-wake at once with the
			// urgent mail first in the digest (newest unread)
			d.urgentHit.Store(false)
			d.logf("urgent interrupt: wake cancelled, re-waking with urgent mail")
			d.urgentNow()
			return
		}
		// Failure rendering stays compact: the full detail (tails included)
		// is archived to the per-account error file on EVERY occurrence,
		// but the terminal line is throttled — a reason repeating since the
		// last line (or within a 30-minute episode) changes nothing in the
		// scrollback. WORKER_VERBOSE=1 echoes the detail every time.
		d.errorLog(err)
		short := shortErr(err)
		if wakeCtx.Err() == context.DeadlineExceeded {
			short = fmt.Sprintf("timeout interrupt after %s (session kept, mail re-queued)",
				time.Duration(d.cfg.TimeoutSec)*time.Second)
		} else if newID != "" {
			short += " · session salvaged"
		}
		d.failLog(short, err)
		board.Set(tag, "waiting", "wake failed: "+short)
		if wakeCtx.Err() != context.DeadlineExceeded {
			// A watchdog timeout on a long-running turn is expected and
			// self-healing (mail re-queued) — not a worker fault, so it
			// stays out of the failure streak.
			d.noteFailure("wake: " + short)
		}
		return
	}
	noticeDone := false
	d.mu.Lock()
	if d.compactPending {
		noticeDone = true
		// notice round done (agent persisted memory). Compact in place when
		// the adapter exposes an entry point (opencode summarize); otherwise
		// just keep the session — every CLI's built-in auto-compact also
		// summarizes in place (session id unchanged). Bare rotation is gone:
		// discarding the whole context loses more than it buys.
		if c, ok := d.adapter.(Compacter); ok && d.sessionID != "" {
			d.mu.Unlock()
			board.Set(tag, "working", "compacting session in place…")
			d.logf("compact: summarizing session %s in place", d.sessionID)
			cctx, ccancel := context.WithTimeout(ctx, 3*time.Minute)
			err := c.CompactSession(cctx, d.cfg, d.sessionID)
			ccancel()
			if err == nil {
				d.logf("compact ok: session %s continues with its summary", d.sessionID)
			} else {
				d.logf("compact in-place failed (%v) — built-in compaction will cover it", err)
			}
			d.mu.Lock()
		} else {
			d.logf("compact: session kept — built-in compaction reduces it in place")
		}
		d.compactPending = false
	}
	d.sessionID = newID // resume chain: id is stable per phase 0, capture anyway
	d.saveState()
	d.mu.Unlock()
	d.failStreak = 0
	// No log line on success: the board row (last ok + session head) and
	// the shutdown report carry the trace; scrollback stays flat.
	board.Set(tag, "waiting", "last ok: "+truncate(newID, 24))

	// compact_notice_tokens: if this wake's context-size report crossed the
	// threshold, the NEXT wake carries the compaction notice; after that
	// round the session is compacted in place (adapter entry point) or left
	// to the CLI's built-in compaction — never rotated. 0 = rely on the
	// CLI's built-in compaction only.
	if !noticeDone && d.cfg.CompactNoticeTokens > 0 && wakeTokens >= d.cfg.CompactNoticeTokens {
		d.compactPending = true
		d.logf("compact: context tokens %d >= notice threshold %d — notice round next", wakeTokens, d.cfg.CompactNoticeTokens)
	}
}

// noteFailure tracks consecutive failures and sends a throttled alert mail.
func (d *Duty) noteFailure(what string) {
	addrs := d.contactAddrs()
	d.mu.Lock()
	d.failStreak++
	streak := d.failStreak
	canAlert := len(addrs) > 0 &&
		streak >= d.cfg.Emergency.FailThreshold &&
		time.Since(d.lastAlert) >= time.Duration(d.cfg.Emergency.ThrottleMin)*time.Minute
	if canAlert {
		d.lastAlert = time.Now()
	}
	d.mu.Unlock()

	if canAlert {
		body := fmt.Sprintf("worker 值守异常告警\n\n账户: %s\n连败: %d 次\n最近错误: %s\n时间: %s\n\n请检查 worker/CLI 环境；未读队列将在恢复后自动追平。",
			d.cfg.Address, streak, what, time.Now().Format(time.RFC3339))
		ok, fail := 0, 0
		for _, to := range addrs {
			if err := d.mail.SendMail(to, "[worker] 值守连败告警", body); err != nil {
				d.logf("alert send error (%s): %v", to, err)
				fail++
			} else {
				ok++
			}
		}
		d.logf("alert sent to %d/%d contacts (streak=%d)", ok, len(addrs), streak)
		_ = fail
	}
}
