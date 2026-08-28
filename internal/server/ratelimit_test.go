package server

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestRegLimiterAllowsUpToLimit(t *testing.T) {
	l := newRegLimiter(time.Hour)
	now := time.Now()
	for i := 0; i < 5; i++ {
		if !l.allow("1.2.3.4", 5, now) {
			t.Fatalf("attempt %d should be allowed (limit 5)", i+1)
		}
	}
	if l.allow("1.2.3.4", 5, now) {
		t.Fatal("6th attempt within the window should be denied")
	}
	// Other IPs are independent.
	if !l.allow("5.6.7.8", 5, now) {
		t.Fatal("different IP should have its own budget")
	}
}

func TestRegLimiterWindowSlides(t *testing.T) {
	l := newRegLimiter(time.Hour)
	t0 := time.Now()
	for i := 0; i < 5; i++ {
		if !l.allow("1.2.3.4", 5, t0) {
			t.Fatalf("attempt %d should be allowed", i+1)
		}
	}
	if l.allow("1.2.3.4", 5, t0.Add(30*time.Minute)) {
		t.Fatal("still within the hour: should be denied")
	}
	if !l.allow("1.2.3.4", 5, t0.Add(61*time.Minute)) {
		t.Fatal("after the window slides, the oldest hits expire and it should be allowed again")
	}
}

func TestRegLimiterZeroDisables(t *testing.T) {
	l := newRegLimiter(time.Hour)
	now := time.Now()
	for i := 0; i < 100; i++ {
		if !l.allow("1.2.3.4", 0, now) {
			t.Fatal("limit 0 means disabled: every attempt allowed")
		}
	}
}

func TestClientIPXFFLastHop(t *testing.T) {
	// Behind the reverse proxy, the LAST XFF entry is the address our
	// infrastructure observed; earlier entries are client-spoofable.
	r := httptest.NewRequest("POST", "/api/register", nil)
	r.RemoteAddr = "10.0.0.5:1234"
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 203.0.113.9")
	if got := clientIP(r); got != "203.0.113.9" {
		t.Errorf("clientIP = %q, want last XFF entry 203.0.113.9", got)
	}
	// No XFF: fall back to the socket address (host only).
	r2 := httptest.NewRequest("POST", "/api/register", nil)
	r2.RemoteAddr = "192.0.2.10:5555"
	if got := clientIP(r2); got != "192.0.2.10" {
		t.Errorf("clientIP = %q, want 192.0.2.10", got)
	}
	// XFF present but empty: still fall back.
	r3 := httptest.NewRequest("POST", "/api/register", nil)
	r3.RemoteAddr = "192.0.2.11:5555"
	r3.Header.Set("X-Forwarded-For", "  ")
	if got := clientIP(r3); got != "192.0.2.11" {
		t.Errorf("clientIP = %q, want 192.0.2.11 (blank XFF falls back)", got)
	}
}
