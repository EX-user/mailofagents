package store

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Boards (kanban/whiteboard) — append-only shared lines behind share codes.
//
// A board is a preamble (single creator-managed line) plus a rolling log of
// content lines. The code in the URL path IS the credential (share-link
// model, no Basic auth for reads/appends): by default create issues ONE
// full-power code; split_codes=true issues a read/write pair instead.
// Lines are append-only with server-side rolling (oldest dropped past the
// board's line cap); the per-line seq is a server-internal monotonic counter
// and is never handed to clients.
//
// Bucket layout:
//
//	boards     : boardID(ULID)          -> Board JSON
//	boardcodes : code(10B base62)       -> boardCodeRef JSON
//	boardlines : boardID -> (nested) seq(8B BE) -> boardLine JSON
//
// Nested per-board line buckets keep rolling (delete oldest) and ordered
// reads (seq ascending) as plain cursor walks.

var (
	bBoards     = []byte("boards")
	bBoardCodes = []byte("boardcodes")
	bBoardLines = []byte("boardlines")
)

// Board sizing/quota constants (kanban API v1.2/v1.3, boss-ruled parameters).
const (
	BoardLineMaxRunes     = 400      // hard cap per content line, counted in runes
	BoardPreambleMaxRunes = 400      // preamble is one line, same cap
	BoardLineCountDefault = 200      // rolling cap when create omits line_count
	BoardLineCountMax     = 500      // rolling cap ceiling
	BoardBytesMax         = 20 << 20 // 20MB content quota per board (boss ruling: per-board, not per-user)
	BoardsPerAccount      = 200      // parallel boards per account (boss ruling)
	BoardCodeLen          = 10       // share-code length, base62
	BoardNameMaxRunes     = 100      // display-only cap; keeps meta lines readable
)

// Errors surfaced to the HTTP layer (mapped to status codes there).
var (
	ErrNoBoard    = errors.New("no such board")
	ErrBoardQuota = errors.New("board quota reached")
	ErrBoardFull  = errors.New("board content quota exceeded")
)

// Board is the stored meta record for one board.
type Board struct {
	ID        string `json:"id"` // ULID; also the lines-bucket name
	Name      string `json:"name"`
	Owner     string `json:"owner"`      // creating account, lowercased
	CreatedAt int64  `json:"created_at"` // unix seconds
	// SingleCode=true: ReadCode is a full-power code and WriteCode is empty.
	// SingleCode=false: ReadCode/WriteCode are the split pair.
	SingleCode bool   `json:"single_code"`
	ReadCode   string `json:"read_code"`
	WriteCode  string `json:"write_code"`
	Preamble   string `json:"preamble"`
	// Display/mute configuration (creator-toggleable via /config; render
	// switches only — they never rewrite stored lines).
	ShowTime bool `json:"show_time"`
	ShowBy   bool `json:"show_by"`
	Muted    bool `json:"muted"`
	// Seq is the server-internal monotonic line counter. It keeps rising
	// across rolling (never reset, never exposed to clients) so the
	// lines-bucket ordering stays stable.
	Seq int64 `json:"seq"`
	// RolledThrough is the highest seq dropped by rolling so far (0 =
	// nothing ever dropped). It lets the ?after anchor operator tell
	// "anchor never existed" (board intact → definitive) apart from
	// "anchor scrolled away" (indistinguishable from a typo once the line
	// is gone → spec says return the full retained content).
	RolledThrough int64 `json:"rolled_through"`
	// LineCount is this board's rolling cap (default 200, max 500).
	LineCount int `json:"line_count"`
	// Lines/Bytes are the current retained content-line count and body
	// bytes (UTF-8, bodies only). Bytes is checked against BoardBytesMax.
	Lines int   `json:"lines"`
	Bytes int64 `json:"bytes"`
}

// Codes returns the board's share codes: one full-power code in single
// mode, [read, write] in split mode.
func (b *Board) Codes() []string {
	if b.SingleCode {
		return []string{b.ReadCode}
	}
	return []string{b.ReadCode, b.WriteCode}
}

// boardCodeRef maps one share code to its board and powers.
type boardCodeRef struct {
	Board string `json:"board"`
	Read  bool   `json:"read"`
	Write bool   `json:"write"`
}

// BoardLine is one retained content line as returned to readers. At is unix
// seconds (append time). By is the appending account address, or "" when the
// append was code-only (anonymous). The seq lives only in the bucket key.
type BoardLine struct {
	Body string `json:"body"`
	At   int64  `json:"at"`
	By   string `json:"by"`
}

// boardLine is the stored form (seq duplicated inside for trim accounting).
type boardLine struct {
	Body string `json:"body"`
	At   int64  `json:"at"`
	By   string `json:"by"`
	Seq  int64  `json:"seq"`
}

func boardLineKey(seq int64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(seq))
	return b[:]
}

// randomCode returns n random base62 characters via rejection sampling —
// 256 % 62 != 0, so plain modulo would weight the first eight alphabet
// characters ~1.3x (review polish; harmless at 62^10, textbook now).
// crypto/rand failure is catastrophic for code uniqueness, so it panics —
// same call as newULID.
func randomCode(n int) string {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	const max = 256 - 256%len(alphabet) // 248: largest multiple ≤ 256
	out := make([]byte, n)
	for i := 0; i < n; {
		raw := make([]byte, n-i)
		if _, err := rand.Read(raw); err != nil {
			panic("store: crypto/rand failed during board code generation: " + err.Error())
		}
		for _, v := range raw {
			if int(v) >= max {
				continue // redraw biased tail
			}
			out[i] = alphabet[int(v)%len(alphabet)]
			i++
			if i == n {
				break
			}
		}
	}
	return string(out)
}

// CreateBoard stores a new board with fresh share codes. ErrBoardQuota when
// the owner already holds BoardsPerAccount boards. Name/lineCount are
// validated by the HTTP layer before this runs.
func (s *Store) CreateBoard(owner, name string, lineCount int, split bool) (*Board, error) {
	now := s.now().Unix()
	var board *Board
	err := s.db.Update(func(tx *bolt.Tx) error {
		// Quota: count the owner's boards inside this same txn so
		// concurrent creates cannot race past the cap.
		count := 0
		c := tx.Bucket(bBoards).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var b Board
			if json.Unmarshal(v, &b) == nil && b.Owner == owner {
				count++
			}
		}
		if count >= BoardsPerAccount {
			return ErrBoardQuota
		}
		b := &Board{
			ID:         newULID(),
			Name:       name,
			Owner:      owner,
			CreatedAt:  now,
			SingleCode: !split,
			LineCount:  lineCount,
		}
		// Codes: one full-power, or a read/write pair. Uniqueness is
		// checked against the codes bucket; at 62^10 the retry loop only
		// guards a pathological RNG.
		need := 1
		if split {
			need = 2
		}
		codes := make([]string, 0, need)
		for len(codes) < need {
			code := randomCode(BoardCodeLen)
			if tx.Bucket(bBoardCodes).Get([]byte(code)) != nil {
				continue
			}
			codes = append(codes, code)
		}
		b.ReadCode = codes[0]
		if split {
			b.WriteCode = codes[1]
		}
		if err := tx.Bucket(bBoards).Put([]byte(b.ID), mustJSON(b)); err != nil {
			return err
		}
		if err := tx.Bucket(bBoardCodes).Put([]byte(b.ReadCode),
			mustJSON(boardCodeRef{Board: b.ID, Read: true, Write: !split})); err != nil {
			return err
		}
		if split {
			if err := tx.Bucket(bBoardCodes).Put([]byte(b.WriteCode),
				mustJSON(boardCodeRef{Board: b.ID, Read: false, Write: true})); err != nil {
				return err
			}
		}
		board = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	return board, nil
}

// BoardByCode resolves a share code to its board and powers. Unknown codes
// read as ErrNoBoard (no distinction worth leaking).
func (s *Store) BoardByCode(code string) (*Board, boardCodeRef, error) {
	var board Board
	var ref boardCodeRef
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bBoardCodes).Get([]byte(code))
		if raw == nil {
			return ErrNoBoard
		}
		if err := json.Unmarshal(raw, &ref); err != nil {
			return err
		}
		return getBoardInTx(tx, ref.Board, &board)
	})
	if err != nil {
		return nil, boardCodeRef{}, err
	}
	return &board, ref, nil
}

// BoardByID loads a board by ID (owner/admin paths).
func (s *Store) BoardByID(id string) (*Board, error) {
	var board Board
	err := s.db.View(func(tx *bolt.Tx) error {
		return getBoardInTx(tx, id, &board)
	})
	if err != nil {
		return nil, err
	}
	return &board, nil
}

// BoardsByOwner lists the owner's boards, oldest first (ULID order).
func (s *Store) BoardsByOwner(owner string) ([]Board, error) {
	var out []Board
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bBoards).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var b Board
			if json.Unmarshal(v, &b) == nil && b.Owner == owner {
				out = append(out, b)
			}
		}
		return nil
	})
	return out, err
}

// AdminBoardPage returns one page of boards (meta only), oldest first.
// page starts at 1; size is caller-clamped by the HTTP layer.
func (s *Store) AdminBoardPage(page, size int) ([]Board, int, error) {
	var out []Board
	total := 0
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bBoards).Cursor()
		skip := (page - 1) * size
		for k, v := c.First(); k != nil; k, v = c.Next() {
			total++
			if total <= skip {
				continue
			}
			if len(out) >= size {
				continue // keep counting total past the page window
			}
			var b Board
			if json.Unmarshal(v, &b) == nil {
				out = append(out, b)
			}
		}
		return nil
	})
	return out, total, err
}

// AnchorStatus classifies the ?after anchor resolution for the response's
// meta hint.
type AnchorStatus string

const (
	// AnchorFound: the anchor matched a retained line; content starts after it.
	AnchorFound AnchorStatus = "found"
	// AnchorNotFound: no retained line matches AND nothing has ever been
	// rolled off, so the anchor definitively never existed on this board —
	// empty content plus the meta hint.
	AnchorNotFound AnchorStatus = "not_found"
	// AnchorRolledPast: no retained line matches but the board has rolled
	// lines off, so the anchor may have scrolled away — spec says treat
	// every retained line as later than the anchor and return the full
	// content.
	AnchorRolledPast AnchorStatus = "rolled_past"
)

// BoardLines reads retained content lines in seq-ascending order.
//   - after != "": the anchor operator (v1.3 pin ②) — case-insensitive
//     substring match, multiple hits take the LAST, content is what follows
//     it. Resolution of a non-matching anchor lands in the returned
//     AnchorStatus (NotFound vs RolledPast per the board's RolledThrough).
//   - match != "": keep only lines whose body contains match
//     (case-insensitive substring). Filtering happens AFTER the anchor cut
//     and BEFORE latestN so the trio reads "after what I saw, matching kw,
//     last N of that".
//   - latestN > 0: only the most recent latestN lines (still ascending).
func (s *Store) BoardLines(boardID string, after string, match string, latestN int) ([]BoardLine, AnchorStatus, error) {
	// Non-nil from the start: an empty result must marshal as [] not null
	// (clients iterate content without null-guards).
	out := []BoardLine{}
	anchor := AnchorFound
	after = strings.ToLower(after)
	match = strings.ToLower(match)
	err := s.db.View(func(tx *bolt.Tx) error {
		boardRaw := tx.Bucket(bBoards).Get([]byte(boardID))
		if boardRaw == nil {
			return ErrNoBoard
		}
		root := tx.Bucket(bBoardLines)
		if root == nil {
			return nil
		}
		bucket := root.Bucket([]byte(boardID))
		if bucket == nil {
			return nil
		}
		c := bucket.Cursor()
		var b Board
		if err := json.Unmarshal(boardRaw, &b); err != nil {
			return err
		}
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var l boardLine
			if err := json.Unmarshal(v, &l); err != nil {
				continue
			}
			// Privacy: with show_by off the owner opted out of exposing
			// who writes — strip by from read responses (storage keeps
			// it; flipping the switch back reveals history again).
			by := l.By
			if !b.ShowBy {
				by = ""
			}
			out = append(out, BoardLine{Body: l.Body, At: l.At, By: by})
		}
		if after != "" {
			cut := -1
			for i, l := range out {
				if strings.Contains(strings.ToLower(l.Body), after) {
					cut = i // multiple hits take the last
				}
			}
			if cut >= 0 {
				out = out[cut+1:]
			} else {
				// No retained match: definitive only when nothing has ever
				// been rolled off this board.
				if b.RolledThrough > 0 {
					anchor = AnchorRolledPast // full content stays
				} else {
					anchor = AnchorNotFound
					out = out[:0]
				}
			}
		}
		if match != "" {
			kept := out[:0]
			for _, l := range out {
				if strings.Contains(strings.ToLower(l.Body), match) {
					kept = append(kept, l)
				}
			}
			out = kept
		}
		if latestN > 0 && len(out) > latestN {
			out = out[len(out)-latestN:]
		}
		return nil
	})
	return out, anchor, err
}

// AppendBoardLine stores one line and applies rolling. ErrBoardFull when the
// line would push the board past BoardBytesMax (quota rejects, never rolls
// away content to make room). The seq counter advances even for lines that
// are immediately rolled off — monotonicity is not tied to retention.
func (s *Store) AppendBoardLine(boardID, body, by string, at time.Time) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		braw := tx.Bucket(bBoards).Get([]byte(boardID))
		if braw == nil {
			return ErrNoBoard
		}
		var b Board
		if err := json.Unmarshal(braw, &b); err != nil {
			return err
		}
		newBytes := b.Bytes + int64(len(body))
		if newBytes > BoardBytesMax {
			return ErrBoardFull
		}
		root := tx.Bucket(bBoardLines)
		bucket, err := root.CreateBucketIfNotExists([]byte(boardID))
		if err != nil {
			return err
		}
		b.Seq++
		l := boardLine{Body: body, At: at.Unix(), By: by, Seq: b.Seq}
		if err := bucket.Put(boardLineKey(b.Seq), mustJSON(l)); err != nil {
			return err
		}
		b.Lines++
		b.Bytes = newBytes
		// Rolling: drop oldest (lowest seq) past the board's cap, adjusting
		// the byte counter by what actually leaves and advancing the
		// roll-through watermark (anchor-operator state).
		for b.Lines > b.LineCount {
			k, v := bucket.Cursor().First()
			if k == nil {
				break
			}
			var old boardLine
			if err := json.Unmarshal(v, &old); err != nil {
				return err
			}
			if err := bucket.Delete(k); err != nil {
				return err
			}
			b.Lines--
			b.Bytes -= int64(len(old.Body))
			if old.Seq > b.RolledThrough {
				b.RolledThrough = old.Seq
			}
			if b.Bytes < 0 {
				b.Bytes = 0
			}
		}
		return tx.Bucket(bBoards).Put([]byte(boardID), mustJSON(b))
	})
}

// SetBoardConfig applies a partial config update (only provided keys
// change). Creator-only enforcement lives in the HTTP layer.
func (s *Store) SetBoardConfig(boardID string, showTime, showBy, muted *bool) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		braw := tx.Bucket(bBoards).Get([]byte(boardID))
		if braw == nil {
			return ErrNoBoard
		}
		var b Board
		if err := json.Unmarshal(braw, &b); err != nil {
			return err
		}
		if showTime != nil {
			b.ShowTime = *showTime
		}
		if showBy != nil {
			b.ShowBy = *showBy
		}
		if muted != nil {
			b.Muted = *muted
		}
		return tx.Bucket(bBoards).Put([]byte(boardID), mustJSON(b))
	})
}

// SetBoardPreamble overwrites the preamble (creator-only enforcement lives
// in the HTTP layer). Single line ≤ BoardPreambleMaxRunes, checked by caller.
func (s *Store) SetBoardPreamble(boardID, body string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		braw := tx.Bucket(bBoards).Get([]byte(boardID))
		if braw == nil {
			return ErrNoBoard
		}
		var b Board
		if err := json.Unmarshal(braw, &b); err != nil {
			return err
		}
		b.Preamble = body
		return tx.Bucket(bBoards).Put([]byte(boardID), mustJSON(b))
	})
}

// DeleteBoard removes the board, its codes and its lines.
func (s *Store) DeleteBoard(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		braw := tx.Bucket(bBoards).Get([]byte(id))
		if braw == nil {
			return ErrNoBoard
		}
		var b Board
		if err := json.Unmarshal(braw, &b); err != nil {
			return err
		}
		for _, code := range b.Codes() {
			// Only delete a code that still points at this board.
			raw := tx.Bucket(bBoardCodes).Get([]byte(code))
			if raw == nil {
				continue
			}
			var ref boardCodeRef
			if json.Unmarshal(raw, &ref) == nil && ref.Board == id {
				if err := tx.Bucket(bBoardCodes).Delete([]byte(code)); err != nil {
					return err
				}
			}
		}
		if err := tx.Bucket(bBoards).Delete([]byte(id)); err != nil {
			return err
		}
		if tx.Bucket(bBoardLines).Bucket([]byte(id)) != nil {
			return tx.Bucket(bBoardLines).DeleteBucket([]byte(id))
		}
		return nil
	})
}

// --- small helpers ---

func getBoardInTx(tx *bolt.Tx, id string, out *Board) error {
	raw := tx.Bucket(bBoards).Get([]byte(id))
	if raw == nil {
		return ErrNoBoard
	}
	return json.Unmarshal(raw, out)
}

func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("boards: marshal %T: %v", v, err))
	}
	return raw
}
