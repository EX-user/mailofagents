package worker

import (
	"regexp"
	"testing"
	"time"
)

func TestLogfTimestampPrefix(t *testing.T) {
	b := &Board{launch: time.Now(), rowEvents: map[string][]string{}, dumpDir: t.TempDir()} // dump mode: ring fills, frames go to files
	b.Logf("acct", "wake failed: %s", "boom")
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.logRing) == 0 {
		t.Fatal("log ring empty")
	}
	line := b.logRing[len(b.logRing)-1]
	if !regexp.MustCompile(`^\[\d{2}:\d{2}:\d{2} acct\] wake failed: boom$`).MatchString(line) {
		t.Fatalf("log line missing HH:MM:SS prefix: %q", line)
	}
}
