package store

import (
	"encoding/json"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Worker heartbeat (boss directive: workers upload waiting/working state
// signals; no frontend, no self-description entry). Only the LATEST beat
// per account is kept — one key, overwritten on every upload.
//
//	workerbeat : address -> WorkerBeat JSON

var bWorkerBeat = []byte("workerbeat")

// WorkerBeat is the latest uploaded signal for one account.
type WorkerBeat struct {
	State  string `json:"state"`   // "waiting" | "working"
	Detail string `json:"detail"`  // free-form tail, capped by the HTTP layer
	At     int64  `json:"at"`      // server receipt time, unix seconds
	SentAt int64  `json:"sent_at"` // client-supplied ts if it sent one
}

// SetWorkerBeat overwrites the account's latest signal.
func (s *Store) SetWorkerBeat(address, state, detail string, sentAt int64, at time.Time) error {
	rec := WorkerBeat{State: state, Detail: detail, At: at.Unix(), SentAt: sentAt}
	val, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bWorkerBeat).Put([]byte(address), val)
	})
}

// WorkerBeatByAddress returns the account's latest signal (ok=false when
// none was ever uploaded).
func (s *Store) WorkerBeatByAddress(address string) (WorkerBeat, bool, error) {
	var rec WorkerBeat
	ok := false
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bWorkerBeat).Get([]byte(address))
		if raw == nil {
			return nil
		}
		ok = true
		return json.Unmarshal(raw, &rec)
	})
	return rec, ok, err
}
