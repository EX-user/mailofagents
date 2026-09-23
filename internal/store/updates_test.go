package store

import (
	"errors"
	"testing"
)

func newUpdatesStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "-updates.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.db.Close() })
	return s
}

// TestUpdatesSeedIdempotent pins EnsureSeedPushes: the first call inserts
// everything, any later call is a no-op (admin content is never overwritten
// by the seed).
func TestUpdatesSeedIdempotent(t *testing.T) {
	s := newUpdatesStore(t)
	seeds := []PushRecord{{Version: "v1", Title: "t1", BodyMD: "b", Published: true}}
	n, err := s.EnsureSeedPushes(seeds)
	if err != nil || n != 1 {
		t.Fatalf("first seed = %d, %v; want 1, nil", n, err)
	}
	// Later seeds (even newer content) change nothing.
	n, err = s.EnsureSeedPushes([]PushRecord{{Version: "v2", Title: "t2", BodyMD: "b", Published: true}})
	if err != nil || n != 0 {
		t.Fatalf("second seed = %d, %v; want 0, nil", n, err)
	}
	all, err := s.ListPushes(true)
	if err != nil || len(all) != 1 || all[0].Version != "v1" {
		t.Fatalf("after reseed list = %v, %v; want only v1", all, err)
	}
}

// TestUpdatesDraftVisibility pins the draft semantics: drafts never win
// LatestPublishedPush, never appear on the self-side list, and never count
// toward the watermark figures.
func TestUpdatesDraftVisibility(t *testing.T) {
	s := newUpdatesStore(t)
	pub, err := s.UpsertPush(PushRecord{Version: "v1", Title: "pub", BodyMD: "b", Published: true})
	if err != nil {
		t.Fatalf("upsert pub: %v", err)
	}
	if pub.PublishedAt == 0 {
		t.Fatal("publishing must stamp PublishedAt")
	}
	if _, err := s.UpsertPush(PushRecord{Version: "v2", Title: "draft", BodyMD: "b"}); err != nil {
		t.Fatalf("upsert draft: %v", err)
	}
	latest, ok, err := s.LatestPublishedPush()
	if err != nil || !ok || latest.Title != "pub" {
		t.Fatalf("latest = %v, %v, %v; want pub", latest, ok, err)
	}
	pubList, err := s.ListPushes(false)
	if err != nil || len(pubList) != 1 {
		t.Fatalf("self list = %v, %v; want 1 published", pubList, err)
	}
	all, err := s.ListPushes(true)
	if err != nil || len(all) != 2 {
		t.Fatalf("admin list = %v, %v; want 2", all, err)
	}
	n, err := s.CountPublishedAfter(0)
	if err != nil || n != 1 {
		t.Fatalf("count after 0 = %d, %v; want 1", n, err)
	}
	// Re-publishing an existing draft keeps its original id and stamps time
	// only if unset.
	drafts, _ := s.ListPushes(true)
	draftID := drafts[1].ID
	republished, err := s.UpsertPush(PushRecord{ID: draftID, Version: "v2", Title: "draft", BodyMD: "b", Published: true})
	if err != nil {
		t.Fatalf("republish: %v", err)
	}
	if republished.ID != draftID || republished.PublishedAt == 0 {
		t.Fatalf("republished = %+v; want same id + stamped PublishedAt", republished)
	}
}

// TestUpdatesMarkReadMonotonic pins the watermark semantics: only forward
// movement, unknown accounts rejected.
func TestUpdatesMarkReadMonotonic(t *testing.T) {
	s := newUpdatesStore(t)
	if _, err := s.CreateAccountWithPassword("u", "t", false, "pw123456"); err != nil {
		t.Fatalf("create: %v", err)
	}
	old := s.now().UnixMilli() - 1000
	recent := s.now().UnixMilli()
	pNew, err := s.UpsertPush(PushRecord{ID: recent, Version: "v2", Title: "n", BodyMD: "b", Published: true})
	if err != nil {
		t.Fatalf("upsert new: %v", err)
	}
	if err := s.MarkPushRead("u@t", pNew.ID); err != nil {
		t.Fatalf("mark new: %v", err)
	}
	// A stale client acking an older id must not lower the watermark.
	if err := s.MarkPushRead("u@t", old); err != nil {
		t.Fatalf("mark old: %v", err)
	}
	got, err := s.LastReadPushID("u@t")
	if err != nil || got != pNew.ID {
		t.Fatalf("watermark = %d, %v; want %d", got, err, pNew.ID)
	}
	// Unknown push ids still advance the watermark (lenient ack), unknown
	// accounts are rejected.
	if err := s.MarkPushRead("nobody@t", 1); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("unknown account = %v; want ErrAccountNotFound", err)
	}
}

// TestUpdatesUpsertValidation pins the field guards: title required, version
// capped.
func TestUpdatesUpsertValidation(t *testing.T) {
	s := newUpdatesStore(t)
	if _, err := s.UpsertPush(PushRecord{BodyMD: "b", Published: true}); err == nil {
		t.Fatal("empty title accepted")
	}
	long := make([]byte, 33)
	for i := range long {
		long[i] = 'v'
	}
	if _, err := s.UpsertPush(PushRecord{Version: string(long), Title: "t", BodyMD: "b"}); err == nil {
		t.Fatal("33-char version accepted")
	}
}
