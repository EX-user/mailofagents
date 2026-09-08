package worker

import (
	"strings"
	"testing"
)

func TestRollWindowWrap(t *testing.T) {
	long := "这是一条超长内容。甲乙丙丁戊己庚辛壬癸ABCDEFGHIJK LMNOPQRSTUVWXYZ0123456789甲乙丙丁戊己庚辛壬癸"
	got := rollWindow([]string{long}, 30, 2)
	for _, l := range got {
		t.Logf("row: %q (cols=%d)", l, visualCols(l))
	}
	if len(got) != 2 {
		t.Fatalf("want 2 rows")
	}
	for _, l := range got {
		if visualCols(l) > 30 {
			t.Errorf("row exceeds width: %q", l)
		}
	}
	if !strings.Contains(got[0], "…") && !strings.Contains(got[1], "…") {
		t.Error("over-long item should mark its hidden head with …")
	}
	// multi short events: one per row, newest last
	got = rollWindow([]string{"first event", "second event"}, 30, 2)
	if got[0] != "first event" || got[1] != "second event" {
		t.Errorf("short events: got %v", got)
	}
	// metering lines skipped (they duplicate the row's ctx readout)
	got = rollWindow([]string{"step_finish | ctx 8k", "real output"}, 30, 2)
	if got[0] != "real output" || got[1] != "" {
		t.Errorf("metering line should be skipped: %v", got)
	}
}
