package worker

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// stubAdapter records the probe wake and simulates a usage-bearing event
// stream by writing the ctx straight onto the board (what the real tee
// does inside adapter.Wake).
type probeAdapter struct {
	gotDigest string
	gotSess   string
	newID     string
}

func (a *probeAdapter) ID() string { return "probe-stub" }
func (a *probeAdapter) Wake(ctx context.Context, cfg *Config, sessionID, digest string) (string, int64, error) {
	a.gotDigest = digest
	a.gotSess = sessionID
	board.SetCtx(localPart(cfg.Address), 39000)
	return a.newID, 1, nil
}
func (a *probeAdapter) Plan(cfg *Config, sessionID, digest string) (string, []string, string, *Config) {
	return "stub", nil, "", cfg
}

func TestCtxProbeOncePerLifetime(t *testing.T) {
	// boss spec 2026-10-01: a never-woken row must still show ctx — one
	// probe round on the first idle poll, never twice.
	cfg := &Config{
		StateFile:  filepath.Join(t.TempDir(), "state.json"),
		Address:    "chief@x",
		TimeoutSec: 30,
	}
	d := NewDuty(cfg, false, false)
	stub := &probeAdapter{newID: "sess-probed"}
	d.adapter = stub
	d.sessionID = "sess-bound"

	d.ctxProbe(context.Background(), "chief", 0, "")
	if !d.ctxProbed {
		t.Fatal("probe flag not set")
	}
	if !strings.Contains(stub.gotDigest, "ctx probe") {
		t.Fatalf("probe digest unexpected: %q", stub.gotDigest)
	}
	if stub.gotSess != "sess-bound" {
		t.Fatalf("probe must resume the bound session, got %q", stub.gotSess)
	}
	if d.sessionID != "sess-probed" {
		t.Fatalf("session rotation not applied: %q", d.sessionID)
	}

	// second idle poll: no second probe (stub would overwrite the digest)
	stub.gotDigest = ""
	d.ctxProbe(context.Background(), "chief", 0, "")
	if stub.gotDigest != "" {
		t.Fatal("probe ran twice")
	}
}
