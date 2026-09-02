package store

import (
	"encoding/json"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// seedMessage writes a message record directly, bypassing Send (whose
// no-valid-recipient guard is exactly what invalid mail has historically
// bypassed via admin sends). Optionally registers index refs under
// refUUID to exercise the sweep.
func seedMessage(t *testing.T, s *Store, m Message, refUUID string) {
	t.Helper()
	if m.ID == "" {
		m.ID = newULID()
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	err = s.DB().Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket(bMessages).Put([]byte(m.ID), b); err != nil {
			return err
		}
		if refUUID != "" {
			key := indexKey(refUUID, m.ID)
			for _, bucket := range [][]byte{bInbox, bUnread, bSent} {
				if err := tx.Bucket(bucket).Put(key, nil); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestInvalidMailClassification(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.DB().Close()
	if _, err := st.CreateAccountWithPassword("live", "t", false, "livepass-123"); err != nil {
		t.Fatalf("create live: %v", err)
	}

	seedMessage(t, st, Message{ID: newULID(), From: "ghost@x", To: []string{"nobody1@t", "nobody2@t"}, Subject: "all missing", ReceivedAt: 100}, "")
	seedMessage(t, st, Message{ID: newULID(), From: "half@x", To: []string{"nobody@t", "live@t"}, Subject: "mixed", ReceivedAt: 90}, "")
	seedMessage(t, st, Message{ID: newULID(), From: "case@x", To: []string{"LIVE@T"}, Subject: "case-insensitive live", ReceivedAt: 80}, "")
	seedMessage(t, st, Message{ID: newULID(), From: "cconly@x", To: nil, CC: []string{"live@t"}, Subject: "cc delivered", ReceivedAt: 70}, "")
	seedMessage(t, st, Message{ID: newULID(), From: "live@t", To: []string{"live@t"}, Subject: "self mail", ReceivedAt: 60}, "")

	list, err := st.ListInvalidMail()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("want exactly 1 invalid (all-TO-missing), got %d: %+v", len(list), list)
	}
	if list[0].Subject != "all missing" {
		t.Fatalf("wrong message classified: %+v", list[0])
	}
}

func TestInvalidMailDeleteSweepsRefs(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.DB().Close()
	if _, err := st.CreateAccountWithPassword("live", "t", false, "livepass-123"); err != nil {
		t.Fatalf("create live: %v", err)
	}

	// Invalid mail that (contrived) carries index refs: deletion must sweep
	// the refs too, not just the body.
	inv := Message{ID: newULID(), From: "ghost@x", To: []string{"nobody@t"}, Subject: "sweep me", ReceivedAt: 5}
	seedMessage(t, st, inv, "0123456789abcdef0123456789abcdef")
	// Mixed mail: listed candidates targeting it must be refused.
	mixed := Message{ID: newULID(), From: "half@x", To: []string{"nobody@t", "live@t"}, Subject: "protected", ReceivedAt: 4}
	seedMessage(t, st, mixed, "")

	n, err := st.DeleteInvalidMail([]string{inv.ID, mixed.ID, "gone@nowhere"}, false)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n != 1 {
		t.Fatalf("deleted %d, want 1 (only the qualifying invalid)", n)
	}

	// Body gone, all three refs swept, mixed mail untouched.
	err = st.DB().View(func(tx *bolt.Tx) error {
		if tx.Bucket(bMessages).Get([]byte(inv.ID)) != nil {
			t.Errorf("body still present")
		}
		if tx.Bucket(bMessages).Get([]byte(mixed.ID)) == nil {
			t.Errorf("mixed mail was deleted — must be protected")
		}
		key := indexKey("0123456789abcdef0123456789abcdef", inv.ID)
		for _, bucket := range [][]byte{bInbox, bUnread, bSent} {
			if tx.Bucket(bucket).Get(key) != nil {
				t.Errorf("ref in %s not swept", bucket)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestInvalidMailDeleteAll(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.DB().Close()
	if _, err := st.CreateAccountWithPassword("live", "t", false, "livepass-123"); err != nil {
		t.Fatalf("create live: %v", err)
	}
	seedMessage(t, st, Message{ID: newULID(), From: "g1@x", To: []string{"nobody@t"}, Subject: "i1"}, "")
	seedMessage(t, st, Message{ID: newULID(), From: "g2@x", To: []string{"nope@t"}, Subject: "i2"}, "")
	seedMessage(t, st, Message{ID: newULID(), From: "k@x", To: []string{"live@t"}, Subject: "keep"}, "")

	n, err := st.DeleteInvalidMail(nil, true)
	if err != nil {
		t.Fatalf("delete all: %v", err)
	}
	if n != 2 {
		t.Fatalf("deleted %d, want 2", n)
	}
	list, err := st.ListInvalidMail()
	if err != nil {
		t.Fatalf("relist: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("list not empty after all-delete: %+v", list)
	}
}

func TestBackupToReopens(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.DB().Close()
	if _, err := st.CreateAccountWithPassword("live", "t", false, "livepass-123"); err != nil {
		t.Fatalf("create live: %v", err)
	}

	path := filepath.Join(st.BackupDir(), "snap-test.db")
	if err := st.BackupTo(path); err != nil {
		t.Fatalf("backup: %v", err)
	}
	snap, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatalf("backup file does not open as bbolt: %v", err)
	}
	defer snap.Close()
	_ = snap.View(func(tx *bolt.Tx) error {
		if tx.Bucket(bAccounts) == nil {
			t.Errorf("accounts bucket missing from snapshot")
		}
		return nil
	})
}
