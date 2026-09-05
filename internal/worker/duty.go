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

	compactBeforeWake bool // -compact-before-wake: compress the bound session once, before this account's first wake of the run

	compactBudget time.Duration // per-run compaction budget; zero = compactTimeout (the tight wake-path default)

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

	// time_beat (clock-scheduled beats, boss spec 2026-09-05): slots are
	// minutes-of-day; a slot crossing sets beatPending (the [报时] line
	// rides the next wake) and — when a wake is in flight and the internal
	// min interval allows — cancels the wake for an immediate beat re-wake.
	beatSlots         []int        // parsed schedule; nil = feature off
	beatPending       atomic.Bool  // a slot crossed: next wake carries the beat line
	beatHit           atomic.Bool  // a slot interrupted the CURRENT wake
	lastSlotCheck     atomic.Int64 // unix nano: last minute boundary the beat logic covered
	lastBeatInterrupt atomic.Int64 // unix nano: last beat INTERRUPT (min-interval anchor)

	// heartbeat upload (waiting/working signals, boss spec 2026-09-05):
	// POSTed to /api/worker/heartbeat; a 404 (server without the endpoint)
	// disables uploads silently for this duty's lifetime.
	hbDisabled atomic.Bool
	hbLast     atomic.Int64 // unix nano: last successful upload
	hbState    string       // last uploaded state (mu)
}

// cliWakeLocks serializes wakes per CLI id within this worker process (see
// the comment at the Wake call site in checkOnce).
var cliWakeLocks sync.Map // cli id -> *sync.Mutex

func NewDuty(cfg *Config, fresh, compactBeforeWake bool) *Duty {
	d := &Duty{
		cfg:               cfg,
		mail:              NewMailClient(cfg.Server, cfg.Address, cfg.Password),
		adapter:           pickAdapter(cfg.CLI),
		fresh:             fresh,
		compactBeforeWake: compactBeforeWake,
		urgentCh:          make(chan struct{}, 1),
	}
	// LoadConfigs already validated the schedule; a parse failure here just
	// means the feature stays off (direct NewDuty callers bypass validation).
	d.beatSlots, _ = ParseTimeBeat(cfg.TimeBeat)
	return d
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

// shortErr compresses a wake failure into one board-friendly line: the
// classified reason instead of the multi-hundred-byte stderr/stdout tails
// (which errorLog archives to the per-account error file).
func shortErr(err error) string {
	s := err.Error()
	// Provider-declared billing/status tokens come FIRST: provider error
	// payloads quote response headers ("connection":"keep-alive"), so the
	// bare "connect" in the network case would swallow real quota errors
	// (S8 ground truth: deepseek 402 wrapped as APIError JSON on stdout).
	switch {
	case strings.Contains(s, "429"), strings.Contains(s, "402"),
		strings.Contains(s, "usage"), strings.Contains(s, "quota"),
		strings.Contains(s, "限额"), strings.Contains(s, "使用上限"),
		// real provider wording (S8 ground truth, 2026-09-05): deepseek
		// reports an exhausted account as "Insufficient Balance" — no
		// "quota"/"429" token anywhere, so match it case-insensitively.
		strings.Contains(strings.ToLower(s), "insufficient"),
		strings.Contains(s, "余额不足"):
		return "provider quota/429"
	case strings.Contains(s, "database is locked"):
		return "opencode db lock (retry queued)"
	case strings.Contains(s, "certificate"), strings.Contains(s, "stream disconnected"),
		strings.Contains(s, "Reconnecting"), strings.Contains(s, "connect"):
		return "network/tls unreachable"
	case strings.Contains(s, "signal: killed"):
		return "timeout kill"
	case strings.Contains(s, "Unknown argument"), strings.Contains(s, "Unknown option"),
		strings.Contains(s, "unknown flag"), strings.Contains(s, "Not enough arguments"):
		return "cli rejected argv (flag/version mismatch?)"
	}
	// Generic exit: drop the embedded tails, keep the head.
	if i := strings.Index(s, "; stderr:"); i > 0 {
		s = s[:i]
	}
	if i := strings.Index(s, "; stdin="); i > 0 {
		s = s[:i]
	}
	return s
}

// errorLog appends a wake failure's FULL detail (tails included) to a
// per-account error file next to the state file, rotated at ~256KB. The
// terminal only ever shows the shortErr line.
func (d *Duty) errorLog(err error) {
	path := filepath.Join(filepath.Dir(d.cfg.StateFile), "errors-"+localPart(d.cfg.Address)+".log")
	if fi, statErr := os.Stat(path); statErr == nil && fi.Size() > 256*1024 {
		_ = os.Rename(path, path+".old")
	}
	// Shadowing hazard (fixed 2026-09-02): this function used `f, err :=`
	// here, which shadowed the PARAMETER err with the OpenFile error — on
	// every successful open the file archived a bare <nil> and the real wake
	// failure was never recorded. Distinct names keep the parameter intact.
	f, openErr := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if openErr != nil {
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
// compactTimeout bounds one in-place compression. Superior note: compaction
// can legitimately take a long while (full-history summarize), so this is
// deliberately generous — the whole point of the flag forms is that the
// wait lands where the operator chose it, not in front of unrelated wakes.
const (
	// compactTimeout bounds one in-place compression on the wake path
	// (-compact-before-wake): the wait lands in front of a wake, keep it
	// tight.
	compactTimeout = 10 * time.Minute
	// compactTimeoutCron is the looser budget for the standalone -compact
	// form: cron-friendly, no wake waits behind it, and a full-history
	// summarize on the team's largest sessions measured ~7min — leave real
	// headroom (budget split endorsed by the architect, 2026-09-03).
	compactTimeoutCron = 25 * time.Minute
)

// compactOnce compresses the bound session IN PLACE, once. opencode ships a
// headless entry (temporary serve → summarize API); CLIs without one keep
// the session — their built-in auto-compact reduces it — because a
// summarize TURN is deliberately NOT used: -compact must never enter
// session generation (superior semantics, three-round finalized design).
// Safe no-op on an empty binding. Before/after lines carry the session id
// and duration; token counters are not available on this path (they ride
// the wake report in the duty loop).
func (d *Duty) compactOnce(ctx context.Context) error {
	d.mu.Lock()
	sess := d.sessionID
	d.mu.Unlock()
	if sess == "" {
		d.logf("compact: no bound session — nothing to compress")
		return nil
	}
	c, ok := d.adapter.(Compacter)
	if !ok {
		d.logf("compact: cli=%s has no headless compact entry — session kept, its built-in auto-compact covers reduction", d.cfg.CLI)
		return nil
	}
	start := time.Now()
	d.logf("compact: compressing session %s in place…", sess)
	budget := d.compactBudget
	if budget <= 0 {
		budget = compactTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	if err := c.CompactSession(cctx, d.cfg, sess); err != nil {
		// the adapter error states whether the session was touched
		// (early failures leave it untouched; a summarize that returned
		// but whose summary went undetected does NOT)
		d.logf("compact FAILED after %s: %v", time.Since(start).Round(time.Second), err)
		return err
	}
	d.logf("compact ok in %s: session %s continues with its summary", time.Since(start).Round(time.Second), sess)
	return nil
}

// CompactOnce is the standalone -compact entry (cmd): load the account's
// state, compress its bound session in place, done. No wake, no session
// generation, no other account is even read.
func CompactOnce(cfg *Config) error {
	d := NewDuty(cfg, false, false)
	d.compactBudget = compactTimeoutCron
	d.loadState()
	return d.compactOnce(context.Background())
}

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
	if d.compactBeforeWake {
		// -compact-before-wake: one in-place compression before this
		// account's first wake. Only THIS account's first turn is delayed —
		// every account runs its own duty loop (goroutine), so cross-account
		// starts are unaffected by design.
		if err := d.compactOnce(ctx); err != nil {
			d.logf("compact-before-wake: %v (continuing into the duty loop; built-in auto-compact remains the net)", err)
		}
	}
	d.lastBeat = time.Now()
	d.startedAt = time.Now()
	d.lastSlotCheck.Store(time.Now().UnixNano())
	go d.refreshContacts(ctx)
	go d.heartbeatLoop(ctx)

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

// watchBeat is the clock twin of watchUrgent: a 1s ticker checks whether a
// scheduled slot crossed since the last covered boundary. A crossing always
// sets beatPending (the [报时] line rides the next wake); it only CANCELS
// the in-flight wake when the internal min interval since the last beat
// interrupt allows — frequent model interruptions are exactly what the
// constant exists to prevent (boss ruling: user-unconfigurable).
func (d *Duty) watchBeat(wakeCtx context.Context, cancel context.CancelFunc, done func()) {
	defer done()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-wakeCtx.Done():
			return
		case <-t.C:
			now := time.Now()
			from := time.Unix(0, d.lastSlotCheck.Load())
			if !beatSlotCrossed(d.beatSlots, from, now) {
				continue
			}
			d.lastSlotCheck.Store(now.UnixNano())
			d.beatPending.Store(true)
			if time.Since(time.Unix(0, d.lastBeatInterrupt.Load())) >= beatMinInterval {
				d.lastBeatInterrupt.Store(now.UnixNano())
				d.beatHit.Store(true)
				d.logf("time-beat: scheduled slot reached, interrupting wake")
				cancel()
				return
			}
			// inside the min interval: no interrupt — the pending line rides
			// the next natural wake instead
		}
	}
}

func (d *Duty) checkOnce(ctx context.Context) {
	tag := localPart(d.cfg.Address)
	// time_beat prologue: consume any minute boundary the poll sleep
	// covered. A crossing always sets beatPending (the [报时] line rides
	// the next wake); whether it also INTERRUPTS an in-flight wake is
	// watchBeat's call during the wake itself.
	now := time.Now()
	if len(d.beatSlots) > 0 {
		from := time.Unix(0, d.lastSlotCheck.Load())
		if beatSlotCrossed(d.beatSlots, from, now) {
			d.beatPending.Store(true)
		}
		d.lastSlotCheck.Store(now.UnixNano())
	}
	unread, err := d.mail.UnreadInbox(50)
	if err != nil {
		short := shortErr(err)
		board.Set(tag, "waiting", "poll failed: "+short)
		d.hb("waiting", "poll failed: "+short)
		d.logf("poll failed: %s", short)
		d.noteFailure("poll: " + short)
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
	d.hb("waiting", fmt.Sprintf("%d unread%s", len(unread), dueMark))
	// Empty-inbox wake suppressors, with three exceptions: compact_pending
	// forces a round (persist memory before compaction), beat_pending
	// forces a round (the [报时] line is the whole point of a clock beat —
	// it must land at its scheduled time, mail or no mail), and an empty
	// session id forces the bootstrap round — onboarding with no mail yet,
	// so the agent orients itself and builds its memory file ahead of the
	// first real mail.
	if len(unread) == 0 && !dutyDue && !d.compactPending && !d.beatPending.Load() && d.sessionID != "" {
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
	d.hb("working", "digest sent")

	var timeBeat string
	switch {
	case d.beatPending.Swap(false):
		timeBeat = fmt.Sprintf("[报时] 定时报时到点（time_beat 档）。当前时间 %s。若你有到点的定时任务请执行；没有则本条无需回信，知悉即可。",
			time.Now().Format("2006-01-02 15:04 -0700"))
	case dutyDue:
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
	var beatDone chan struct{}
	if len(d.beatSlots) > 0 {
		beatDone = make(chan struct{})
		go d.watchBeat(wakeCtx, cancel, func() { close(beatDone) })
	}
	// Serialize wakes per CLI within this worker process: opencode keeps a
	// single global SQLite session store per user, so two concurrent
	// spawns collide on "database is locked" at init (multi-account
	// same-poll wake). Cross-process collisions are out of scope — run one
	// worker per host, or give each account its own XDG data dir.
	muAny, _ := cliWakeLocks.LoadOrStore(d.cfg.CLI, &sync.Mutex{})
	wakeMu := muAny.(*sync.Mutex)
	wakeMu.Lock()
	// Wake heartbeat (0.2.5.1 #7): one line per minute while the CLI runs,
	// so a long wake shows life signs instead of a static row. The line
	// distinguishes "model is streaming" (events advancing) from "no
	// output for a while" (worded suspicious, never confirmed hung —
	// diagnosing is the human's call). Wakes under a minute never print
	// one: scrollback stays flat on the healthy path.
	hbStart := time.Now()
	hbStop := make(chan struct{})
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-hbStop:
				return
			case <-wakeCtx.Done():
				return
			case <-tick.C:
				events, age, ok := wakeEventStats(tag)
				board.Set(tag, "working", heartbeatLine(time.Since(hbStart), events, age, ok))
			}
		}
	}()
	newID, wakeTokens, err := func() (string, int64, error) {
		defer wakeMu.Unlock()
		return d.adapter.Wake(wakeCtx, d.cfg, d.sessionID, Digest(d.cfg, unread, resumed, timeBeat, compactNotice, stats, statsErr == nil))
	}()
	close(hbStop)
	<-hbDone
	if urgentDone != nil {
		cancel() // wake over: the watcher exits via its wakeCtx select
		<-urgentDone
	}
	if beatDone != nil {
		cancel()
		<-beatDone
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
		if d.beatHit.Swap(false) {
			// time-beat interrupt (boss spec 2026-09-05): a scheduled clock
			// slot crossed mid-wake; re-wake at once with the [报时] line —
			// beatPending was set by watchBeat before the cancel.
			d.logf("time-beat interrupt: wake cancelled, re-waking with beat line")
			d.urgentNow()
			return
		}
		// Failure display: the compact classified reason goes on the reused
		// board row (and one short log line); the full stderr/stdout tails
		// archive to the per-account error file.
		d.errorLog(err)
		short := shortErr(err)
		if wakeCtx.Err() == context.DeadlineExceeded {
			short = fmt.Sprintf("timeout interrupt after %s (session kept, mail re-queued)",
				time.Duration(d.cfg.TimeoutSec)*time.Second)
		} else if newID != "" {
			short += " · session salvaged"
		}
		board.Set(tag, "waiting", "wake failed: "+short)
		d.hb("waiting", "wake failed: "+short)
		d.logf("wake failed: %s", short)
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
			d.hb("working", "compacting session in place")
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
	d.hb("waiting", "last ok: "+truncate(newID, 24))

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

// heartbeatLine renders the periodic in-wake status line. Stale or absent
// output is worded as "suspicious" on purpose: from the outside a silent
// model turn and a wedged process look identical, and the worker only
// reports the observation, never the diagnosis.
func heartbeatLine(elapsed time.Duration, events int64, lastEventAge time.Duration, wakeRunning bool) string {
	if !wakeRunning {
		return fmt.Sprintf("wake %s · alive · no events reported yet", elapsed.Round(time.Second))
	}
	if events == 0 {
		return fmt.Sprintf("wake %s · alive · events=0 · NO events %s (suspicious)",
			elapsed.Round(time.Second), elapsed.Round(time.Second))
	}
	line := fmt.Sprintf("wake %s · alive · events=%d · last event %s ago",
		elapsed.Round(time.Second), events, lastEventAge.Round(time.Second))
	if lastEventAge >= 2*time.Minute {
		line += " (suspicious)"
	}
	return line
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
