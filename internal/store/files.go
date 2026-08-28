package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Attachment system (v0.5): uploaded files live in two buckets — bFiles
// holds lightweight metadata (scans stay cheap), bFileData holds the raw
// content under the same id. Download authorization: the requester must be
// the owner or on the file's allowed list AND present the file's access
// code. TTL evicts both halves.
var (
	bFiles    = []byte("files")
	bFileData = []byte("files_data")
)

// Limits (Phase 1 constants; the total cap is admin-tunable via settings).
const (
	FileMaxBytes     = 1 << 20  // 1MB per file
	FileQuotaPerAcct = 20 << 20 // 20MB per account
	FileTTL          = 30 * 24 * time.Hour
)

// FileRecord is the metadata half of an uploaded file.
type FileRecord struct {
	ID         string   `json:"id"`       // ULID
	Owner      string   `json:"owner"`    // uploader address
	Filename   string   `json:"filename"` // original name (sanitized on use)
	Size       int64    `json:"size"`
	AccessCode string   `json:"access_code"` // random hex, required at download
	Allowed    []string `json:"allowed"`     // addresses that may download
	CreatedAt  int64    `json:"created_at"`
}

// ErrFileNotFound / ErrQuotaExceeded are surfaced by the store and mapped
// by the handlers (404 / 413).
var (
	ErrFileNotFound  = fmt.Errorf("file not found")
	ErrQuotaExceeded = fmt.Errorf("storage quota exceeded")
)

func fileDataKey(id string) []byte { return []byte(id + ":d") }

// SaveFile stores metadata + content in one transaction (either both land
// or neither). Quota: the owner's live files' total size plus this file
// must stay under FileQuotaPerAcct; the caller enforces the per-file cap
// before reading the whole body.
func (s *Store) SaveFile(owner, filename string, allowed []string, content []byte) (*FileRecord, error) {
	now := s.now()
	id := newULID()
	code, err := randomFileCode()
	if err != nil {
		return nil, err
	}
	rec := FileRecord{
		ID:         id,
		Owner:      owner,
		Filename:   filename,
		Size:       int64(len(content)),
		AccessCode: code,
		Allowed:    allowed,
		CreatedAt:  now.Unix(),
	}
	meta, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		fb := tx.Bucket(bFiles)
		// Quota checks: per-account AND the global total cap.
		var used, total int64
		c := fb.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var fr FileRecord
			if json.Unmarshal(v, &fr) == nil {
				total += fr.Size
				if fr.Owner == owner {
					used += fr.Size
				}
			}
		}
		if used+rec.Size > s.GetFileQuotaPerAcct() {
			return ErrQuotaExceeded
		}
		if total+rec.Size > s.GetFilesTotalLimit() {
			return ErrQuotaExceeded
		}
		if err := fb.Put([]byte(id), meta); err != nil {
			return err
		}
		return tx.Bucket(bFileData).Put(fileDataKey(id), content)
	})
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// GetFileMeta returns the metadata without content.
func (s *Store) GetFileMeta(id string) (*FileRecord, error) {
	var rec FileRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bFiles).Get([]byte(id))
		if raw == nil {
			return ErrFileNotFound
		}
		return json.Unmarshal(raw, &rec)
	})
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// GetFileContent returns the raw bytes.
func (s *Store) GetFileContent(id string) ([]byte, error) {
	var content []byte
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bFileData).Get(fileDataKey(id))
		if raw == nil {
			return ErrFileNotFound
		}
		content = append([]byte(nil), raw...)
		return nil
	})
	return content, err
}

// AuthorizeFileDownload: exists + (owner or allowed) + code match.
func (s *Store) AuthorizeFileDownload(requester, id, code string) (*FileRecord, error) {
	rec, err := s.GetFileMeta(id)
	if err != nil {
		return nil, err
	}
	permitted := strings.EqualFold(rec.Owner, requester)
	if !permitted {
		for _, a := range rec.Allowed {
			if strings.EqualFold(a, requester) {
				permitted = true
				break
			}
		}
	}
	if !permitted || !secureEqual(rec.AccessCode, code) {
		// Same error for both failures: no oracle about which check failed.
		return nil, ErrFileNotFound
	}
	return rec, nil
}

// CleanupExpiredFiles removes files older than the TTL. Returns the number
// evicted. Runs at startup and daily.
func (s *Store) CleanupExpiredFiles() (int, error) {
	cutoff := s.now().Add(-FileTTL).Unix()
	var doomed [][]byte
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bFiles).ForEach(func(k, v []byte) error {
			var fr FileRecord
			if json.Unmarshal(v, &fr) != nil || fr.CreatedAt < cutoff {
				// Corrupt records are swept too — they are unreachable anyway.
				doomed = append(doomed, append([]byte(nil), k...))
			}
			return nil
		})
	})
	if err != nil {
		return 0, err
	}
	if len(doomed) == 0 {
		return 0, nil
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		fb := tx.Bucket(bFiles)
		db_ := tx.Bucket(bFileData)
		for _, k := range doomed {
			var fr FileRecord
			if raw := fb.Get(k); raw != nil && json.Unmarshal(raw, &fr) == nil {
				_ = db_.Delete(fileDataKey(fr.ID))
			}
			if err := fb.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
	return len(doomed), err
}

// SendWithAttachments composes a mail whose attachments reference files
// previously uploaded by the sender. One transaction does everything:
// validates each file id (must exist and be owned by the sender — an
// account can never attach someone else's file), snapshots the file
// metadata into the message (what recipients need to download), extends
// each file's allowed list with the message's valid recipients, and runs
// the ordinary send. attachIDs may be empty (then it behaves like Send).
func (s *Store) SendWithAttachments(from, fromName string, to []string, cc []string, subject, body string, attachIDs []string, inReplyTo string) (*SendResult, error) {
	msgID := newULID()
	now := s.now().Unix()
	delivered := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		// Same in-transaction parent check as Send (one Get, miss -> fail).
		if inReplyTo != "" && tx.Bucket(bMessages).Get([]byte(inReplyTo)) == nil {
			return ErrNoSuchParent
		}
		// Resolve attachments first: all must be the sender's own files.
		var metas []AttachmentMeta
		fb := tx.Bucket(bFiles)
		var fileKeys [][]byte
		for _, id := range attachIDs {
			raw := fb.Get([]byte(id))
			if raw == nil {
				return fmt.Errorf("attachment %q not found", id)
			}
			var fr FileRecord
			if err := json.Unmarshal(raw, &fr); err != nil {
				return fmt.Errorf("attachment %q unreadable", id)
			}
			if !strings.EqualFold(fr.Owner, from) {
				return fmt.Errorf("attachment %q is not owned by sender", id)
			}
			metas = append(metas, AttachmentMeta{ID: fr.ID, Filename: fr.Filename, Size: fr.Size, AccessCode: fr.AccessCode, CreatedAt: fr.CreatedAt})
			fileKeys = append(fileKeys, []byte(id))
		}

		// Determine the valid recipients up front (mirrors Send's skip
		// semantics) so allowed-list extension matches actual delivery.
		// CC recipients are delivered (and granted attachment access) like
		// To recipients; To/CC overlap dedups to a single copy.
		var validRecipients []string
		seen := map[string]bool{}
		for _, addr := range append(append([]string{}, to...), cc...) {
			l := strings.ToLower(addr)
			if seen[l] {
				continue
			}
			seen[l] = true
			if _, err := getAccountInTx(tx, addr); err == nil {
				validRecipients = append(validRecipients, addr)
			}
		}
		if len(validRecipients) == 0 {
			return fmt.Errorf("no valid recipients among %v (cc: %v)", to, cc)
		}

		// Extend each file's allowed list with the recipients (dedup, owner
		// address skipped — the owner can always download their own files).
		for i, key := range fileKeys {
			raw := fb.Get(key)
			var fr FileRecord
			if err := json.Unmarshal(raw, &fr); err != nil {
				return err
			}
			set := map[string]bool{}
			for _, a := range fr.Allowed {
				set[strings.ToLower(a)] = true
			}
			changed := false
			for _, addr := range validRecipients {
				l := strings.ToLower(addr)
				if !set[l] && !strings.EqualFold(fr.Owner, addr) {
					set[l] = true
					changed = true
				}
			}
			if changed {
				fr.Allowed = make([]string, 0, len(set))
				for a := range set {
					fr.Allowed = append(fr.Allowed, a)
				}
				sortStrings(fr.Allowed)
				val, err := json.Marshal(fr)
				if err != nil {
					return err
				}
				if err := fb.Put(key, val); err != nil {
					return err
				}
			}
			_ = i
		}

		// Compose and store the message with attachment metadata.
		msg := Message{
			ID:          msgID,
			From:        from,
			To:          to,
			CC:          cc,
			InReplyTo:   inReplyTo,
			Subject:     subject,
			Body:        body,
			Attachments: metas,
			ReceivedAt:  now,
		}
		msgBytes, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		mb := tx.Bucket(bMessages)
		if existing := mb.Get([]byte(msgID)); existing != nil {
			return nil // idempotency guard (same as Send)
		}
		if err := mb.Put([]byte(msgID), msgBytes); err != nil {
			return err
		}
		ib := tx.Bucket(bInbox)
		ub := tx.Bucket(bUnread)
		for _, addr := range validRecipients {
			acc, err := getAccountInTx(tx, addr)
			if err != nil {
				continue
			}
			key := indexKey(acc.UUID, msgID)
			if err := ib.Put(key, nil); err != nil {
				return err
			}
			if err := ub.Put(key, nil); err != nil {
				return err
			}
			delivered++
		}
		sb := tx.Bucket(bSent)
		if sender, err := getAccountInTx(tx, from); err == nil {
			key := indexKey(sender.UUID, msgID)
			if err := sb.Put(key, nil); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}
	if delivered == 0 {
		return nil, fmt.Errorf("no valid recipients among %v", to)
	}
	return &SendResult{MessageID: msgID}, nil
}

func sortStrings(list []string) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j] < list[j-1]; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

// FileSummary is one row of the account's attachment list (the panel's
// attachment management card). ExpiresAt is derived: CreatedAt + FileTTL.
type FileSummary struct {
	ID        string `json:"id"`
	Filename  string `json:"filename"`
	Size      int64  `json:"size"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at"`
}

// ListAccountFiles returns the owner's files sorted by expiry ascending
// (oldest CreatedAt first — expiry is a fixed offset of it). Expired but
// not-yet-swept files are included: the sweep will remove them shortly,
// and until then showing them is more honest than hiding them.
func (s *Store) ListAccountFiles(owner string) ([]FileSummary, error) {
	var out []FileSummary
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bFiles).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var fr FileRecord
			if json.Unmarshal(v, &fr) != nil || !strings.EqualFold(fr.Owner, owner) {
				continue
			}
			out = append(out, FileSummary{
				ID:        fr.ID,
				Filename:  fr.Filename,
				Size:      fr.Size,
				CreatedAt: fr.CreatedAt,
				ExpiresAt: fr.CreatedAt + int64(FileTTL.Seconds()),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, nil
}

// DeleteFile removes the owner's file (metadata + content) in one
// transaction. A foreign or missing id returns ErrFileNotFound — the
// caller must not learn whether the id exists. Quota is reclaimed
// implicitly (usage is derived from live records). Sent messages keep
// their snapshot metadata; their download links simply 404 afterwards.
func (s *Store) DeleteFile(id, owner string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		fb := tx.Bucket(bFiles)
		raw := fb.Get([]byte(id))
		if raw == nil {
			return ErrFileNotFound
		}
		var fr FileRecord
		if json.Unmarshal(raw, &fr) != nil || !strings.EqualFold(fr.Owner, owner) {
			return ErrFileNotFound
		}
		if err := tx.Bucket(bFileData).Delete(fileDataKey(fr.ID)); err != nil {
			return err
		}
		return fb.Delete([]byte(id))
	})
}

// ExtendFile renews the owner's file: expiry is overwritten to now+FileTTL
// (renewal semantics, not accumulation — implemented as CreatedAt = now,
// since expiry is derived from it). A foreign or missing id returns
// ErrFileNotFound (same masquerade as delete). Sent messages keep their
// snapshot metadata, so old download links keep working for the renewed
// window too. Returns the new absolute expiry (unix seconds).
func (s *Store) ExtendFile(id, owner string) (int64, error) {
	var expires int64
	err := s.db.Update(func(tx *bolt.Tx) error {
		fb := tx.Bucket(bFiles)
		raw := fb.Get([]byte(id))
		if raw == nil {
			return ErrFileNotFound
		}
		var fr FileRecord
		if json.Unmarshal(raw, &fr) != nil || !strings.EqualFold(fr.Owner, owner) {
			return ErrFileNotFound
		}
		fr.CreatedAt = s.now().Unix()
		expires = fr.CreatedAt + int64(FileTTL.Seconds())
		val, err := json.Marshal(fr)
		if err != nil {
			return err
		}
		return fb.Put([]byte(id), val)
	})
	return expires, err
}

// AccountFilesUsed returns the total bytes of the account's live files.
func (s *Store) AccountFilesUsed(owner string) int64 {
	used, _, _ := s.AccountFileStats(owner)
	return used
}

// AccountFileStats scans the account's live files once and returns the
// used bytes, the file count, and how many expire within the next 7 days
// (Overview attach column; expiry = CreatedAt + FileTTL — the same clock
// the sweep uses).
func (s *Store) AccountFileStats(owner string) (used int64, count int, expiringSoon int) {
	now := s.now().Unix()
	soonWindow := (FileTTL - 7*24*time.Hour).Seconds()
	_ = s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bFiles).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var fr FileRecord
			if json.Unmarshal(v, &fr) == nil && strings.EqualFold(fr.Owner, owner) {
				used += fr.Size
				count++
				if age := now - fr.CreatedAt; age >= int64(soonWindow) && age < int64(FileTTL.Seconds()) {
					expiringSoon++
				}
			}
		}
		return nil
	})
	return used, count, expiringSoon
}

// randomFileCode mints a 32-hex-char download code.
func randomFileCode() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// secureEqual compares two short hex strings without early exit.
func secureEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}
