// Package audit records security-relevant actions on the server: account
// registration, password verification, and message sends. It logs NO message
// bodies — only action, account, and a short non-sensitive detail — so the
// trail is safe to expose to the admin.
//
// Storage is the same bbolt file as the message store (shared handle). Each
// entry lives under a monotonically increasing big-endian key.
package audit

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Action enumerates auditable operations.
type Action string

const (
	ActionRegister             Action = "register"
	ActionAuthenticate         Action = "authenticate"
	ActionSend                 Action = "send"
	ActionReadInbox            Action = "read_inbox"
	ActionGetMessage           Action = "get_message"
	ActionResetPassword        Action = "reset_password"
	ActionDisableAccount       Action = "disable_account"
	ActionProfileUpdate        Action = "profile_update"
	ActionSetRegistration      Action = "set_registration"
	ActionSetDirectoryListed   Action = "set_directory_listed"
	ActionSetOneclickRegister  Action = "set_oneclick_register"
	ActionSetRandomRegister    Action = "set_random_register"
	ActionNormalizeAccountCase Action = "normalize_account_case"
	ActionSetShowcase          Action = "set_showcase"
	ActionClearShowcase        Action = "clear_showcase"
	ActionDeleteShowcaseItem   Action = "delete_showcase_item"
	ActionSetDanmaku           Action = "set_danmaku"
	ActionFileUpload           Action = "file_upload"
	ActionFileDownload         Action = "file_download"
	ActionFileDelete           Action = "file_delete"
	ActionFileExtend           Action = "file_extend"
	ActionRegisterTeam         Action = "register_team"
	ActionSubDeclare           Action = "sub_declare"
	ActionSubRevoke            Action = "sub_revoke"
	ActionSubRead              Action = "sub_read"
	ActionSubRemoved           Action = "sub_removed"
	ActionAuthTokenIssue       Action = "auth_token_issue"     // v0.6.27 remember-login mint (alice's enum-precision note)
	ActionAuthTokenRevoke      Action = "auth_token_revoke"    // logout / password-change revocation
	ActionPushSubscribe        Action = "push_subscribe"       // v0.6.30 web push registration
	ActionDisplayLocalChange   Action = "display_local_change" // v0.6.34 display local-part update
	ActionSetSiteCopy          Action = "set_site_copy"        // v0.1.2 admin brand copy update
	ActionPushRevoke           Action = "push_revoke"          // subscription removal
	ActionInvalidMailBackup    Action = "invalid_mail_backup"  // pre-mass-delete db snapshot
	ActionInvalidMailDelete    Action = "invalid_mail_delete"  // admin purge of all-TO-missing mail
)

// Entry is one audit record.
type Entry struct {
	ID        int64     `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Action    Action    `json:"action"`
	Account   string    `json:"account"`
	Detail    string    `json:"detail"`
}

type recordValue struct {
	TS      int64  `json:"ts"`
	Action  string `json:"action"`
	Account string `json:"account"`
	Detail  string `json:"detail"`
}

var bucket = []byte("audit")

// Store is the audit persistence layer. It shares a bbolt handle with the
// message store.
type Store struct {
	db  *bolt.DB
	now func() time.Time
}

// New attaches the audit bucket to db (creating it if needed).
func New(db *bolt.DB) (*Store, error) {
	s := &Store{db: db, now: time.Now}
	err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bucket)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("init audit bucket: %w", err)
	}
	return s, nil
}

// Record appends an audit entry. ctx is accepted for future cancellation but
// bbolt does not currently honor it.
func (s *Store) Record(ctx context.Context, action Action, account, detail string) error {
	val, err := json.Marshal(recordValue{
		TS:      s.now().Unix(),
		Action:  string(action),
		Account: account,
		Detail:  detail,
	})
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		id, err := tx.Bucket(bucket).NextSequence()
		if err != nil {
			return err
		}
		return tx.Bucket(bucket).Put(itob(int64(id)), val)
	})
}

// List returns the most recent limit entries (newest first). limit<=0 → 100.
func (s *Store) List(ctx context.Context, limit int) ([]Entry, error) {
	if limit <= 0 {
		limit = 100
	}
	out := make([]Entry, 0, limit)
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucket).Cursor()
		for k, v := c.Last(); k != nil; k, v = c.Prev() {
			var rv recordValue
			if err := json.Unmarshal(v, &rv); err != nil {
				continue
			}
			out = append(out, Entry{
				ID:        btoi(k),
				Timestamp: time.Unix(rv.TS, 0),
				Action:    Action(rv.Action),
				Account:   rv.Account,
				Detail:    rv.Detail,
			})
			if len(out) >= limit {
				break
			}
		}
		return nil
	})
	return out, err
}

func itob(v int64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(v))
	return b[:]
}

func btoi(b []byte) int64 {
	if len(b) < 8 {
		return 0
	}
	return int64(binary.BigEndian.Uint64(b[:8]))
}
