package server

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// regLimiter is a fixed-window, in-memory per-IP registration throttle.
// The portal offers friction-free registration, so scripted mass account
// creation needs a stopper; this is intentionally simple (no persistence,
// no coordination — restart clears the counters, which is fine for this
// purpose).
type regLimiter struct {
	mu     sync.Mutex
	hits   map[string][]time.Time
	window time.Duration
}

func newRegLimiter(window time.Duration) *regLimiter {
	return &regLimiter{hits: map[string][]time.Time{}, window: window}
}

// allow records one attempt for ip and reports whether it is still within
// limit for the current window. Failed attempts (e.g. name conflicts) count
// too — that blocks scripted name-probing as well as mass creation.
func (l *regLimiter) allow(ip string, limit int, now time.Time) bool {
	if limit <= 0 {
		return true // limit disabled
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-l.window)
	var kept []time.Time
	for _, t := range l.hits[ip] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= limit {
		l.hits[ip] = kept
		return false
	}
	l.hits[ip] = append(kept, now)
	// Opportunistic global cleanup so the map cannot grow without bound.
	if len(l.hits) > 4096 {
		for k, v := range l.hits {
			var kk []time.Time
			for _, t := range v {
				if t.After(cutoff) {
					kk = append(kk, t)
				}
			}
			if len(kk) == 0 {
				delete(l.hits, k)
			} else {
				l.hits[k] = kk
			}
		}
	}
	return true
}

// clientIP returns the best-guess originating client IP for rate limiting.
// Production sits behind a reverse proxy that terminates TLS: the proxy
// appends the address it saw to X-Forwarded-For, so the LAST entry is the
// one our infrastructure observed (earlier entries are client-spoofable).
// Without the header, fall back to the socket address.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
