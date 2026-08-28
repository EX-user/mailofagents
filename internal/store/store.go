// Package store is agentmail's embedded message store. It is a single bbolt
// file holding accounts, messages, and per-account inbox/sent indexes. The
// server process owns the only open handle; the gateway never touches storage
// directly (it talks to the server over HTTP).
//
// Storage model (see docs/architecture.md):
//
//	accounts  : address                 -> Account (JSON)
//	messages  : ulid                    -> Message (JSON)   [the data body]
//	inbox     : uuid(16B) + ulid(26B)   -> ""               [index: who sees it]
//	sent      : uuid(16B) + ulid(26B)   -> ""               [index: who sent it]
//
// Messages are stored once; inbox/sent hold references. Deleting an inbox
// entry means "this account no longer sees this message"; the message body is
// left for a future GC pass. This is the mailbox model from real mail servers
// (each recipient gets a logical copy) implemented space-efficiently.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Bucket names.
var (
	bAccounts = []byte("accounts")
	bMessages = []byte("messages")
	bInbox    = []byte("inbox")
	bSent     = []byte("sent")
	bUnread   = []byte("unread") // key: uuid(32 hex) + ulid(26) -> exists = unread for that account
	bMeta     = []byte("meta")   // system metadata (initialized flag, domain, ...)
	bSubs     = []byte("subs")   // subordinate-relationship graph: superior\x00subordinate -> SubRecord
	bTokens   = []byte("tokens")   // session tokens, keyed by SHA-256 hash (v0.6.27 remember-login)
	bPushSubs = []byte("pushsubs") // web push subscriptions: sha256(endpoint) -> PushSubscription (v0.6.30)
	bPushDND  = []byte("pushdnd")  // per-account notification do-not-disturb windows (v0.6.30)
)

// Meta keys within the meta bucket.
var (
	mInitialized         = []byte("initialized")
	mDomain              = []byte("domain")
	mListen              = []byte("listen")
	mRegistrationEnabled  = []byte("registration_enabled")
	mDirectoryListedEnabled = []byte("directory_listed_enabled")
	mSendRateLimit        = []byte("send_rate_limit")
	mByteRateLimit        = []byte("byte_rate_limit")
	mRegisterIPRateLimit  = []byte("register_ip_rate_limit")
	mFilesTotalLimit      = []byte("files_total_limit")
	mFileQuotaPerAcct     = []byte("file_quota_per_acct")
	mOneclickRegisterEnabled = []byte("oneclick_register_enabled")
	// mRandomRegisterEnabled gates the PASSWORDLESS /api/register path
	// (the retired one-click random register). Absent = disabled: the
	// mechanism retired with a superior directive, so the default must
	// stay off even on instances that carry old oneclick UI-hint values.
	mRandomRegisterEnabled = []byte("random_register_enabled")
	mShowcaseEnabled      = []byte("showcase_enabled")
	mDanmakuMode          = []byte("danmaku_default_mode")
	mDanmakuSpeed         = []byte("danmaku_default_speed")
	mDanmakuCount         = []byte("danmaku_default_count")
)

// Store wraps a bbolt database with agentmail's operations.
type Store struct {
	db  *bolt.DB
	now func() time.Time
}

// Open opens (or creates) the bbolt database at path and initializes buckets.
func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open bbolt %q: %w", path, err)
	}
	s := &Store{db: db, now: time.Now}
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bAccounts, bMessages, bInbox, bSent, bUnread, bMeta, bShowcase, bFiles, bFileData, bSubs, bTokens, bPushSubs, bPushDND} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return fmt.Errorf("create bucket %q: %w", b, err)
			}
		}
		return nil
	}); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// DB returns the underlying bbolt handle so other stores (e.g. audit) can
// share the same database file. The handle stays owned by this Store; callers
// must not close it.
func (s *Store) DB() *bolt.DB { return s.db }

// --- key helpers ---

// indexKey builds the inbox/sent key: 16-byte hex UUID + 26-char ULID.
// The UUID prefix groups a recipient's messages together; the ULID tail makes
// them sort chronologically within that group.
func indexKey(uuidHex, ulid string) []byte {
	out := make([]byte, len(uuidHex)+len(ulid))
	copy(out, uuidHex)
	copy(out[len(uuidHex):], ulid)
	return out
}

// hexID returns 16 random bytes as a 32-char hex string. Used as an account's
// internal UUID.
func hexID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("store: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// CountAccounts returns the total number of accounts. Used by the admin stats.
func (s *Store) CountAccounts() (int, error) {
	n := 0
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bAccounts).Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			n++
		}
		return nil
	})
	return n, err
}

// CountMessages returns the total number of stored message bodies. Note this
// counts unique messages, not inbox/sent references.
func (s *Store) CountMessages() (int, error) {
	n := 0
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bMessages).Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			n++
		}
		return nil
	})
	return n, err
}

// --- system metadata (meta bucket) ---

// IsInitialized reports whether the system has been bootstrapped (admin
// account created via setup wizard or config migration).
func (s *Store) IsInitialized() bool {
	var ok bool
	_ = s.db.View(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bMeta)
		if mb == nil {
			return nil
		}
		ok = string(mb.Get(mInitialized)) == "1"
		return nil
	})
	return ok
}

// SetInitialized marks the system as bootstrapped.
func (s *Store) SetInitialized() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bMeta)
		if mb == nil {
			return nil
		}
		return mb.Put(mInitialized, []byte("1"))
	})
}

// GetDomain returns the system domain set during bootstrap, or "" if unset.
func (s *Store) GetDomain() string {
	var d string
	_ = s.db.View(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bMeta)
		if mb == nil {
			return nil
		}
		if v := mb.Get(mDomain); v != nil {
			d = string(v)
		}
		return nil
	})
	return d
}

// SetDomain persists the system domain (used during bootstrap).
func (s *Store) SetDomain(domain string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bMeta)
		if mb == nil {
			return nil
		}
		return mb.Put(mDomain, []byte(domain))
	})
}

// GetListen returns the listen address set during bootstrap, or "" if unset.
func (s *Store) GetListen() string {
	var d string
	_ = s.db.View(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bMeta)
		if mb == nil {
			return nil
		}
		if v := mb.Get(mListen); v != nil {
			d = string(v)
		}
		return nil
	})
	return d
}

// SetListen persists the listen address (used during bootstrap).
func (s *Store) SetListen(listen string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bMeta)
		if mb == nil {
			return nil
		}
		return mb.Put(mListen, []byte(listen))
	})
}

// --- rate / registration settings ---

// IsRegistrationEnabled reports whether public account registration is
// allowed. Defaults to true (no meta value = enabled).
func (s *Store) IsRegistrationEnabled() bool {
	var v string
	_ = s.db.View(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bMeta)
		if mb == nil {
			return nil
		}
		if raw := mb.Get(mRegistrationEnabled); raw != nil {
			v = string(raw)
		}
		return nil
	})
	return v != "0" // absent or "1" = enabled
}

// SetRegistrationEnabled toggles public registration.
func (s *Store) SetRegistrationEnabled(enabled bool) error {
	v := "1"
	if !enabled {
		v = "0"
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bMeta)
		if mb == nil {
			return nil
		}
		return mb.Put(mRegistrationEnabled, []byte(v))
	})
}

// IsRandomRegisterEnabled reports whether the PASSWORDLESS register path
// (one-click random register, retired from the public UI) may still be
// used. Defaults to FALSE — off unless an admin explicitly re-enables it
// for debugging. Note this is a server-side GATE, unlike the legacy
// oneclick_register_enabled key which was a pure UI hint.
func (s *Store) IsRandomRegisterEnabled() bool {
	var v string
	_ = s.db.View(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bMeta)
		if mb == nil {
			return nil
		}
		if raw := mb.Get(mRandomRegisterEnabled); raw != nil {
			v = string(raw)
		}
		return nil
	})
	return v == "1" // absent = disabled (retired mechanism)
}

// SetRandomRegisterEnabled toggles the passwordless debug register path.
func (s *Store) SetRandomRegisterEnabled(enabled bool) error {
	v := "1"
	if !enabled {
		v = "0"
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bMeta)
		if mb == nil {
			return nil
		}
		return mb.Put(mRandomRegisterEnabled, []byte(v))
	})
}

// DBSizeBytes returns the current size of the bbolt database file via a
// filesystem stat. 0 if unavailable (the field is informational).
func (s *Store) DBSizeBytes() int64 {
	p := s.db.Path()
	if p == "" {
		return 0
	}
	fi, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// metaBool reads a boolean meta flag; absent = def.
func (s *Store) metaBool(key []byte, def bool) bool {
	var raw []byte
	_ = s.db.View(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bMeta)
		if mb == nil {
			return nil
		}
		if v := mb.Get(key); v != nil {
			raw = append([]byte(nil), v...)
		}
		return nil
	})
	if raw == nil {
		return def
	}
	return string(raw) != "0"
}

// setMetaBool writes a boolean meta flag.
func (s *Store) setMetaBool(key []byte, value bool) error {
	v := "1"
	if !value {
		v = "0"
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bMeta)
		if mb == nil {
			return nil
		}
		return mb.Put(key, []byte(v))
	})
}

// IsOneclickRegisterEnabled reports whether the portal's one-click agent
// register is offered. Defaults to true. (Frontend hides the button when
// off; the register API itself keeps working — this gates the UX, not
// registration.)
func (s *Store) IsOneclickRegisterEnabled() bool {
	return s.metaBool(mOneclickRegisterEnabled, true)
}

// SetOneclickRegisterEnabled toggles the one-click register UX.
func (s *Store) SetOneclickRegisterEnabled(enabled bool) error {
	return s.setMetaBool(mOneclickRegisterEnabled, enabled)
}

// IsShowcaseEnabled reports whether the Compose "public showcase" checkbox
// is offered in the panel. Defaults to true. Purely a UI hint per admin's
// clarification: it does NOT gate the tee or the showcase endpoint.
func (s *Store) IsShowcaseEnabled() bool {
	return s.metaBool(mShowcaseEnabled, true)
}

// SetShowcaseEnabled toggles the showcase.
func (s *Store) SetShowcaseEnabled(enabled bool) error {
	return s.setMetaBool(mShowcaseEnabled, enabled)
}

// metaStr reads a string meta flag; absent or not in allowed = def.
func (s *Store) metaStr(key []byte, def string, allowed []string) string {
	var raw string
	_ = s.db.View(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bMeta)
		if mb == nil {
			return nil
		}
		if v := mb.Get(key); v != nil {
			raw = string(v)
		}
		return nil
	})
	for _, a := range allowed {
		if raw == a {
			return raw
		}
	}
	return def
}

// Danmaku defaults for the portal's showcase danmaku (server default;
// browsers may override via localStorage).
var (
	danmakuModes      = []string{"A", "B"}
	danmakuSpeeds     = []string{"slow", "medium", "fast"}
	danmakuCounts     = []string{"few", "normal", "more"}
)

func (s *Store) GetDanmakuDefaultMode() string   { return s.metaStr(mDanmakuMode, "A", danmakuModes) }
func (s *Store) GetDanmakuDefaultSpeed() string  { return s.metaStr(mDanmakuSpeed, "medium", danmakuSpeeds) }
func (s *Store) GetDanmakuDefaultCount() string  { return s.metaStr(mDanmakuCount, "normal", danmakuCounts) }

// SetDanmakuDefaults validates and persists any provided danmaku default.
// Empty string leaves that field unchanged.
func (s *Store) SetDanmakuDefaults(mode, speed, count string) error {
	if mode != "" {
		if !containsStr(danmakuModes, mode) {
			return fmt.Errorf("mode must be A or B")
		}
	}
	if speed != "" {
		if !containsStr(danmakuSpeeds, speed) {
			return fmt.Errorf("speed must be slow, medium or fast")
		}
	}
	if count != "" {
		if !containsStr(danmakuCounts, count) {
			return fmt.Errorf("count must be few, normal or more")
		}
	}
	set := func(key []byte, val string) error {
		if val == "" {
			return nil
		}
		return s.db.Update(func(tx *bolt.Tx) error {
			mb := tx.Bucket(bMeta)
			if mb == nil {
				return nil
			}
			return mb.Put(key, []byte(val))
		})
	}
	if err := set(mDanmakuMode, mode); err != nil {
		return err
	}
	if err := set(mDanmakuSpeed, speed); err != nil {
		return err
	}
	return set(mDanmakuCount, count)
}

func containsStr(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// IsDirectoryListedEnabled reports whether accounts are allowed to opt
// themselves into the public directory (set Visible=true). Defaults to true
// (no meta value = enabled), so existing installations keep working.
func (s *Store) IsDirectoryListedEnabled() bool {
	var v string
	_ = s.db.View(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bMeta)
		if mb == nil {
			return nil
		}
		if raw := mb.Get(mDirectoryListedEnabled); raw != nil {
			v = string(raw)
		}
		return nil
	})
	return v != "0" // absent or "1" = enabled
}

// SetDirectoryListedEnabled toggles whether accounts may list themselves.
func (s *Store) SetDirectoryListedEnabled(enabled bool) error {
	v := "1"
	if !enabled {
		v = "0"
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bMeta)
		if mb == nil {
			return nil
		}
		return mb.Put(mDirectoryListedEnabled, []byte(v))
	})
}

// GetSendRateLimit returns the per-account send limit per hour. Default 500.
func (s *Store) GetSendRateLimit() int {
	v := s.getMetaInt(mSendRateLimit, 500)
	if v <= 0 {
		return 500
	}
	return v
}

// SetSendRateLimit sets the per-account send limit per hour.
func (s *Store) SetSendRateLimit(n int) error {
	return s.setMetaInt(mSendRateLimit, n)
}

// GetByteRateLimit returns the per-account byte receive limit per hour. Default 1MB.
func (s *Store) GetByteRateLimit() int64 {
	v := s.getMetaInt64(mByteRateLimit, 1048576)
	if v <= 0 {
		return 1048576
	}
	return v
}

// SetByteRateLimit sets the per-account byte receive limit per hour.
func (s *Store) SetByteRateLimit(n int64) error {
	return s.setMetaInt64(mByteRateLimit, n)
}

// GetRegisterIPRateLimit returns the per-IP registration attempt limit per
// hour. Default 5 — the portal offers friction-free registration, so
// scripted mass account creation needs a stopper. 0 disables the limit.
func (s *Store) GetRegisterIPRateLimit() int {
	v := s.getMetaInt(mRegisterIPRateLimit, 5)
	if v < 0 {
		return 5
	}
	return v
}

// SetRegisterIPRateLimit sets the per-IP registration attempt limit per
// hour (0 disables).
func (s *Store) SetRegisterIPRateLimit(n int) error {
	return s.setMetaInt(mRegisterIPRateLimit, n)
}

// GetFilesTotalLimit returns the total byte cap for ALL stored attachment
// files. Default 512MB; 0 is treated as the default (the cap cannot be
// disabled — disk reclamation depends on it).
func (s *Store) GetFilesTotalLimit() int64 {
	v := s.getMetaInt64(mFilesTotalLimit, 512<<20)
	if v <= 0 {
		return 512 << 20
	}
	return v
}

// SetFilesTotalLimit sets the total attachment byte cap.
func (s *Store) SetFilesTotalLimit(n int64) error {
	return s.setMetaInt64(mFilesTotalLimit, n)
}

// GetFileQuotaPerAcct returns the per-account attachment byte quota.
// Default 20MB; <= 0 falls back to the default (the quota cannot be
// disabled — a single account could otherwise exhaust the total cap).
func (s *Store) GetFileQuotaPerAcct() int64 {
	v := s.getMetaInt64(mFileQuotaPerAcct, FileQuotaPerAcct)
	if v <= 0 {
		return FileQuotaPerAcct
	}
	return v
}

// SetFileQuotaPerAcct sets the per-account attachment byte quota.
func (s *Store) SetFileQuotaPerAcct(n int64) error {
	return s.setMetaInt64(mFileQuotaPerAcct, n)
}

// getMetaInt / getMetaInt64 / setMetaInt / setMetaInt64 are small helpers for
// reading/writing integer settings in the meta bucket.
func (s *Store) getMetaInt(key []byte, def int) int {
	return int(s.getMetaInt64(key, int64(def)))
}

func (s *Store) getMetaInt64(key []byte, def int64) int64 {
	var v int64 = def
	_ = s.db.View(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bMeta)
		if mb == nil {
			return nil
		}
		if raw := mb.Get(key); raw != nil {
			var n int64
			if _, err := fmt.Sscanf(string(raw), "%d", &n); err == nil {
				v = n
			}
		}
		return nil
	})
	return v
}

func (s *Store) setMetaInt(key []byte, n int) error {
	return s.setMetaInt64(key, int64(n))
}

func (s *Store) setMetaInt64(key []byte, n int64) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bMeta)
		if mb == nil {
			return nil
		}
		return mb.Put(key, []byte(fmt.Sprintf("%d", n)))
	})
}
