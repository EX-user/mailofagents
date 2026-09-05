package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Heartbeat signal upload (boss spec 2026-09-05): the worker POSTs its
// waiting/working state — the same face the local status board shows — to
// a dedicated server endpoint every minute (and immediately on state
// changes). No frontend yet; the endpoint stays OUT of /api/self. Servers
// without the endpoint answer 404: uploads then disable silently for this
// duty's lifetime, keeping old server deployments noise-free.

const (
	hbUploadInterval = time.Minute
	hbEndpoint       = "/api/worker/heartbeat"
)

// hb mirrors one board.Set: the state signal follows the board face.
func (d *Duty) hb(state, detail string) {
	if d.hbDisabled.Load() {
		return
	}
	now := time.Now()
	if state == d.hbState && now.UnixNano()-d.hbLast.Load() < hbUploadInterval.Nanoseconds() {
		return // unchanged state inside the keepalive window
	}
	payload, _ := json.Marshal(map[string]any{
		"address": d.cfg.Address,
		"state":   state,
		"detail":  truncate(detail, 200),
		"ts":      now.Unix(),
	})
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(d.cfg.Server, "/")+hbEndpoint, bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.SetBasicAuth(d.cfg.Address, d.cfg.Password) // full address: short address 401s
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return // transient: skip this tick, keep trying on later ticks
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		d.hbDisabled.Store(true) // server predates the endpoint: silent degrade
	case resp.StatusCode == http.StatusOK:
		d.hbLast.Store(now.UnixNano())
		d.mu.Lock()
		d.hbState = state
		d.mu.Unlock()
	}
	// other statuses: skip; the next tick retries
}

// heartbeatLoop keeps a keepalive flowing even when the poll loop sits
// still (idle accounts are exactly the ones a monitoring face must see
// alive).
func (d *Duty) heartbeatLoop(ctx context.Context) {
	t := time.NewTicker(hbUploadInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.hb("waiting", "keepalive")
		}
	}
}
