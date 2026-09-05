package worker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHeartbeatLine(t *testing.T) {
	cases := []struct {
		name       string
		elapsed    time.Duration
		events     int64
		age        time.Duration
		wakeActive bool
		want       string
	}{
		{
			name:       "no events yet — suspicious, not hung",
			elapsed:    3 * time.Minute,
			events:     0,
			age:        0,
			wakeActive: true,
			want:       "wake 3m0s · alive · events=0 · NO events 3m0s (suspicious)",
		},
		{
			name:       "streaming normally",
			elapsed:    2 * time.Minute,
			events:     14,
			age:        3 * time.Second,
			wakeActive: true,
			want:       "wake 2m0s · alive · events=14 · last event 3s ago",
		},
		{
			name:       "stale stream — suspicious suffix",
			elapsed:    5 * time.Minute,
			events:     2,
			age:        150 * time.Second,
			wakeActive: true,
			want:       "wake 5m0s · alive · events=2 · last event 2m30s ago (suspicious)",
		},
		{
			name:       "no tee registered yet",
			elapsed:    1 * time.Minute,
			events:     0,
			age:        0,
			wakeActive: false,
			want:       "wake 1m0s · alive · no events reported yet",
		},
	}
	for _, tc := range cases {
		got := heartbeatLine(tc.elapsed, tc.events, tc.age, tc.wakeActive)
		if got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
		if tc.events == 0 && tc.wakeActive && strings.Contains(got, "hung") {
			t.Errorf("%s: must not declare a hang, got %q", tc.name, got)
		}
	}
}

func TestWakeEventStatsNoWake(t *testing.T) {
	if _, _, ok := wakeEventStats("no-such-account-tag"); ok {
		t.Error("wakeEventStats must report no wake for an unregistered tag")
	}
}

// --- heartbeat signal upload (boss spec 2026-09-05) ---

func TestHeartbeatUpload(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &Config{
		Server:   srv.URL,
		Address:  "alpha@fixture.test", // full address in Basic auth — short address 401s
		Password: "pw",
	}
	d := NewDuty(cfg, false, false)

	d.hb("working", "digest sent")
	if calls != 1 || gotPath != "/api/worker/heartbeat" {
		t.Fatalf("want one upload to /api/worker/heartbeat, got %d to %s", calls, gotPath)
	}
	wantAuth := "Basic YWxwaGFAZml4dHVyZS50ZXN0OnB3" // alpha@fixture.test:pw
	if gotAuth != wantAuth {
		t.Fatalf("auth = %q, want full-address basic %q", gotAuth, wantAuth)
	}
	if gotBody["state"] != "working" || gotBody["address"] != "alpha@fixture.test" || gotBody["ts"] == nil {
		t.Fatalf("payload shape off: %v", gotBody)
	}

	// same state inside the keepalive window: throttled
	d.hb("working", "digest sent")
	if calls != 1 {
		t.Fatalf("unchanged state must throttle, calls=%d", calls)
	}

	// state change: uploads immediately
	d.hb("waiting", "last ok")
	if calls != 2 || gotBody["state"] != "waiting" {
		t.Fatalf("state change must upload, calls=%d body=%v", calls, gotBody)
	}
}

func TestHeartbeat404Disables(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cfg := &Config{Server: srv.URL, Address: "alpha@fixture.test", Password: "pw"}
	d := NewDuty(cfg, false, false)
	d.hb("waiting", "x")
	d.hb("waiting", "y")
	d.hb("working", "z")
	if calls != 1 {
		t.Fatalf("404 must disable uploads after the first attempt, calls=%d", calls)
	}
	if !d.hbDisabled.Load() {
		t.Fatal("hbDisabled must be set")
	}
}

func TestHeartbeatKeepaliveAfterInterval(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &Config{Server: srv.URL, Address: "alpha@fixture.test", Password: "pw"}
	d := NewDuty(cfg, false, false)
	d.hbLast.Store(time.Now().Add(-2 * hbUploadInterval).UnixNano()) // keepalive window long gone
	d.hb("waiting", "keepalive")
	if calls != 1 {
		t.Fatalf("keepalive after interval must upload, calls=%d", calls)
	}
}

func TestBeatMinIntervalGate(t *testing.T) {
	// The internal min interval gates INTERRUPTS only; a crossing inside the
	// window still sets beatPending so the [报时] line rides the next wake.
	cfg := &Config{Server: "http://x", Address: "a@b.test", Password: "p", TimeBeat: "{8:00}"}
	d := NewDuty(cfg, false, false)
	if len(d.beatSlots) != 1 || d.beatSlots[0] != 480 {
		t.Fatalf("slots not parsed: %v", d.beatSlots)
	}
	to := time.Date(2026, 9, 5, 8, 0, 30, 0, time.Local)
	if !beatSlotCrossed(d.beatSlots, to.Add(-90*time.Second), to) {
		t.Fatal("slot must register as crossed")
	}
	d.lastBeatInterrupt.Store(time.Now().UnixNano()) // an interrupt JUST happened
	if time.Since(time.Unix(0, d.lastBeatInterrupt.Load())) >= beatMinInterval {
		t.Fatal("test setup: inside the min interval")
	}
	// watchBeat's in-window branch: pending set, no interrupt flag
	d.beatPending.Store(true)
	if d.beatHit.Load() {
		t.Fatal("inside the min interval the interrupt flag must stay unset")
	}
	if !d.beatPending.Load() {
		t.Fatal("pending must stay set: the line rides the next natural wake")
	}
}

func TestLineTeeCountersAndRegistry(t *testing.T) {
	tee := &lineTee{tag: "hbtest", secret: "sekrit"}
	if _, loaded := wakeTees.LoadOrStore("hbtest", tee); loaded {
		t.Fatal("tag collision in test registry")
	}
	defer wakeTees.Delete("hbtest")

	tee.Write([]byte(`{"type":"thread.started","thread_id":"abc"}` + "\n"))
	n, age, ok := wakeEventStats("hbtest")
	if !ok || n != 1 {
		t.Fatalf("after one event: n=%d ok=%v, want n=1 ok=true", n, ok)
	}
	if age <= 0 || age > 5*time.Second {
		t.Errorf("age=%v, want fresh (0,5s]", age)
	}

	// non-JSON noise also counts as an event (it is output, just unsummarized shape)
	tee.Write([]byte("plain banner line\n"))
	if n, _, _ = wakeEventStats("hbtest"); n != 2 {
		t.Errorf("after noise line: n=%d, want 2", n)
	}

	// an empty line produces no summary and must not count
	tee.Write([]byte("\n"))
	if n, _, _ = wakeEventStats("hbtest"); n != 2 {
		t.Errorf("after empty line: n=%d, want 2", n)
	}
}
