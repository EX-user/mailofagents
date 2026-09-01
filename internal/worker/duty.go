package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
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

	mu        sync.Mutex
	sessionID string // current bound session ("" = start a new one next wake)
	failStreak int
	lastAlert  time.Time
	lastBeat   time.Time // duty-window anchor: process start or last time-beat wake
	startedAt  time.Time // process start, for the uptime stamp in heartbeats
}

func NewDuty(cfg *Config, fresh bool) *Duty {
	return &Duty{
		cfg:     cfg,
		mail:    NewMailClient(cfg.Server, cfg.Address, cfg.Password),
		adapter: pickAdapter(cfg.CLI),
		fresh:   fresh,
	}
}

// logf prefixes every duty log line with the account local-part so
// parallel account loops stay distinguishable in one stream.
func (d *Duty) logf(format string, args ...any) {
	log.Printf("["+localPart(d.cfg.Address)+"] "+format, args...)
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

	t := time.NewTicker(time.Duration(d.cfg.PollIntervalSec) * time.Second)
	defer t.Stop()
	for {
		d.checkOnce(ctx)
		select {
		case <-ctx.Done():
			d.logf("duty stop: %v", ctx.Err())
			return
		case <-t.C:
		}
	}
}

// cleanWorkdir implements -fresh: drop the stored session binding and the
// worker-created artifacts (state file next to the config; pi session store
// plus the legacy state file in the workdir) so the next wake starts a
// brand-new session. Files the agent (or the user) created for other
// purposes are deliberately left alone.
func (d *Duty) cleanWorkdir() {
	d.logf("fresh start: ignoring stored session, cleaning worker artifacts")
	_ = os.Remove(d.statePath())
	_ = os.Remove(filepath.Join(d.cfg.Workdir, ".worker-state.json")) // legacy location
	if err := os.RemoveAll(filepath.Join(d.cfg.Workdir, ".pi-sessions")); err == nil {
		d.logf("fresh: removed .pi-sessions")
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

func (d *Duty) checkOnce(ctx context.Context) {
	start := time.Now()
	unread, err := d.mail.UnreadInbox(50)
	if err != nil {
		d.logf("poll error (%dms): %v", time.Since(start).Milliseconds(), err)
		d.noteFailure("poll: " + err.Error())
		return
	}
	dutyDue := d.dutyDue()
	// Heartbeat line every round: the loop is alive, here's the queue size.
	dueMark := ""
	if dutyDue {
		dueMark = " [duty window due]"
	}
	d.logf("poll: %d unread (%dms) up %s%s", len(unread), time.Since(start).Milliseconds(),
		time.Since(d.startedAt).Round(time.Second), dueMark)
	if len(unread) == 0 && !dutyDue {
		d.failStreak = 0
		return
	}
	if len(unread) > 0 {
		d.logf("wake: %d unread (session %q)", len(unread), d.sessionID)
	} else {
		d.logf("wake: time beat (session %q)", d.sessionID)
	}

	var timeBeat string
	if dutyDue {
		timeBeat = fmt.Sprintf("[报时] 值守已连续运行 %s，当前时间 %s。若你有到点的定时任务请执行；没有则本条无需回信，知悉即可。",
			time.Since(d.lastBeat).Round(time.Second), time.Now().Format("2006-01-02 15:04:05 -0700"))
		d.lastBeat = time.Now()
	}

	wakeCtx, cancel := context.WithTimeout(ctx, time.Duration(d.cfg.TimeoutSec)*time.Second)
	defer cancel()
	resumed := d.sessionID != ""
	newID, err := d.adapter.Wake(wakeCtx, d.cfg, d.sessionID, Digest(d.cfg, unread, resumed, timeBeat))
	if ctx.Err() != nil {
		return // shutting down; do not count as failure
	}
	if err != nil {
		d.logf("wake error: %v", err)
		d.noteFailure("wake: " + err.Error())
		return
	}
	d.mu.Lock()
	d.sessionID = newID // resume chain: id is stable per phase 0, capture anyway
	d.saveState()
	d.mu.Unlock()
	d.failStreak = 0
	d.logf("wake ok: session=%s", newID)
}

// noteFailure tracks consecutive failures and sends a throttled alert mail.
func (d *Duty) noteFailure(what string) {
	d.mu.Lock()
	d.failStreak++
	streak := d.failStreak
	canAlert := d.cfg.Alert.To != "" &&
		streak >= d.cfg.Alert.FailThreshold &&
		time.Since(d.lastAlert) >= time.Duration(d.cfg.Alert.ThrottleMin)*time.Minute
	if canAlert {
		d.lastAlert = time.Now()
	}
	d.mu.Unlock()

	if canAlert {
		body := fmt.Sprintf("worker 值守异常告警\n\n账户: %s\n连败: %d 次\n最近错误: %s\n时间: %s\n\n请检查 worker/CLI 环境；未读队列将在恢复后自动追平。",
			d.cfg.Address, streak, what, time.Now().Format(time.RFC3339))
		if err := d.mail.SendMail(d.cfg.Alert.To, "[worker] 值守连败告警", body); err != nil {
			d.logf("alert send error: %v", err)
		} else {
			d.logf("alert sent to %s (streak=%d)", d.cfg.Alert.To, streak)
		}
	}
}
