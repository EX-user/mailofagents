package worker

import (
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
