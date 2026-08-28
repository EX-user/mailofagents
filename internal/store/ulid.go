package store

import (
	"crypto/rand"
	"encoding/binary"
	"strings"
	"time"
)

// ULID is a 26-character Crockford Base32 string encoding a 128-bit value:
// 48 bits of millisecond timestamp + 80 bits of cryptographic randomness.
// It is time-ordered (so lexical sort matches chronological sort) and
// collision-resistant without coordination — ideal for message IDs stored in
// a bbolt bucket whose iteration order matters.
//
// This is a minimal pure-Go implementation with no external dependency. It is
// NOT a conformant ULID library (no monotonicity guarantees across calls in
// the same millisecond), but for a single-writer bbolt store that is fine:
// the random tail disambiguates within-millisecond IDs, and the writer is
// serialized by bbolt transactions anyway.
//
// See https://github.com/ulid/spec for the format.

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// IsULID reports whether s is a 26-char Crockford Base32 string (the exact
// alphabet newULID emits — single source of truth for write-side format
// checks). A hand-rolled range check once dropped 'Q' between N-P and R-T
// and rejected ~56% of REAL ids flakily; deriving from the constant makes
// that class of bug impossible.
func IsULID(s string) bool {
	if len(s) != 26 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune(crockford, rune(s[i])) {
			return false
		}
	}
	return true
}

// newULID returns a fresh ULID string for the current time.
func newULID() string {
	return newULIDAt(time.Now())
}

// newULIDAt returns a ULID string for the given time. Split out so tests can
// pin the timestamp.
func newULIDAt(now time.Time) string {
	var b [16]byte
	// 48-bit big-endian millisecond timestamp.
	ms := uint64(now.UnixMilli())
	binary.BigEndian.PutUint64(b[0:8], ms) // writes 8 bytes; we use first 6
	// Copy the low 48 bits (6 bytes) into b[0:6], overwriting the high 2.
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	// 80 bits of randomness.
	if _, err := rand.Read(b[6:16]); err != nil {
		// crypto/rand failure is catastrophic for ID uniqueness. Panicking is
		// safer than silently emitting a predictable ID.
		panic("store: crypto/rand failed during ULID generation: " + err.Error())
	}
	return encodeULID(b)
}

// encodeULID encodes 16 bytes into the 26-character Crockford Base32 form.
func encodeULID(b [16]byte) string {
	// ULID encodes the 48-bit time as 10 chars and the 80-bit random as 16
	// chars, for 26 total. We process 5-bit groups left to right.
	dst := make([]byte, 26)
	// Time part: 6 bytes -> 10 chars (top 4 bits of first char are zero).
	dst[0] = crockford[(b[0]&224)>>5]
	dst[1] = crockford[b[0]&31]
	dst[2] = crockford[(b[1]&248)>>3]
	dst[3] = crockford[((b[1]&7)<<2)|((b[2]&192)>>6)]
	dst[4] = crockford[(b[2]&62)>>1]
	dst[5] = crockford[((b[2]&1)<<4)|((b[3]&240)>>4)]
	dst[6] = crockford[((b[3]&15)<<1)|((b[4]&128)>>7)]
	dst[7] = crockford[(b[4]&124)>>2]
	dst[8] = crockford[((b[4]&3)<<3)|((b[5]&224)>>5)]
	dst[9] = crockford[b[5]&31]
	// Random part: 10 bytes -> 16 chars, processed as 5-bit groups.
	r := b[6:16] // 80 bits
	var bits uint64
	var n uint
	j := 10
	for i := 0; i < len(r); i++ {
		bits = (bits << 8) | uint64(r[i])
		n += 8
		for n >= 5 {
			n -= 5
			dst[j] = crockford[(bits>>n)&31]
			j++
		}
	}
	// One trailing group from the remaining bits (n < 5, padded with zeros).
	if n > 0 {
		dst[j] = crockford[(bits<<(5-n))&31]
	}
	return string(dst)
}
