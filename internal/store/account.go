package store

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	bolt "go.etcd.io/bbolt"
	"golang.org/x/crypto/bcrypt"
)

// ErrAccountExists is returned when CreateAccount is called for an address
// that already exists.
var ErrAccountExists = errors.New("account already exists")

// ErrAccountNotFound is returned when an account lookup misses.
var ErrAccountNotFound = errors.New("account not found")

// ErrAccountDisabled is returned when a disabled account tries to authenticate.
var ErrAccountDisabled = errors.New("account disabled")

// Account is the stored record for a mail account.
type Account struct {
	UUID         string `json:"uuid"`
	Address      string `json:"address"`
	PasswordHash []byte `json:"password_hash"`
	IsAdmin      bool   `json:"is_admin"`
	Disabled     bool   `json:"disabled"`
	CreatedAt    int64  `json:"created_at"` // unix seconds
	// Visible controls whether the account appears in the public directory
	// (query=directory). Defaults to false; old records missing this field
	// unmarshal to false, so no data migration is needed.
	Visible bool `json:"visible"`
	// Signature is a short user-supplied tagline shown next to the address in
	// the public directory. Empty by default.
	Signature string `json:"signature"`
	// Prefs holds small per-account UI preferences ({"audio_autoplay":
	// false, "image_preview": true}). Nil on old records — readers treat
	// nil as all-defaults, no migration needed. Keys are whitelist-
	// validated at the API edge; the store accepts whatever map it is
	// given (it never interprets the contents).
	Prefs map[string]any `json:"prefs,omitempty"`
	// DisplayLocal is the account's optional case-preserved spelling of its
	// own local part, shown ONLY on the settings page / self-query (V06
	// ruling: mail surfaces stay all-lowercase). Empty = unset. Invariant:
	// strings.ToLower(DisplayLocal) == local part of Address.
	DisplayLocal string `json:"display_local,omitempty"`
}

// CreateAccountResult is returned by CreateAccount.
type CreateAccountResult struct {
	Address  string
	Password string // plaintext, returned once to the caller
	UUID     string
}

// CreateAccount creates a new account with the given local-part under domain.
// It returns the generated address and plaintext password (available only
// here — the store keeps a bcrypt hash). Returns ErrAccountExists if the
// address is taken.
func (s *Store) CreateAccount(name, domain string, isAdmin bool) (*CreateAccountResult, error) {
	return s.createAccountWithPassword(name, domain, isAdmin, generatePassword(24))
}

// CreateAccountWithPassword is like CreateAccount but lets the caller supply
// the plaintext password (used by the setup wizard / bootstrap).
func (s *Store) CreateAccountWithPassword(name, domain string, isAdmin bool, password string) (*CreateAccountResult, error) {
	return s.createAccountWithPassword(name, domain, isAdmin, password)
}

func (s *Store) createAccountWithPassword(name, domain string, isAdmin bool, password string) (*CreateAccountResult, error) {
	name = strings.TrimSpace(name)
	// Normalize to lowercase so Foo@ and foo@ can't become two accounts (the
	// bSubs graph already lowercases its keys; the accounts bucket did not,
	// and an outside user reported exactly this double-listing). All entry
	// points funnel through here, so this is the single fix point.
	address := strings.ToLower(name + "@" + domain)

	// Preserve the caller's original casing as the display local-part (V06
	// ruling): key stays lowercase, the user sees their own spelling on the
	// settings page. Only set when the caller actually used uppercase.
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	uuid := hexID()
	acc := Account{
		UUID:         uuid,
		Address:      address,
		PasswordHash: hash,
		IsAdmin:      isAdmin,
		CreatedAt:    s.now().Unix(),
	}
	if display := strings.TrimSpace(name); display != "" && display != strings.ToLower(display) {
		acc.DisplayLocal = display
	}
	val, err := json.Marshal(acc)
	if err != nil {
		return nil, err
	}

	err = s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bAccounts)
		if existing := b.Get([]byte(address)); existing != nil {
			return ErrAccountExists
		}
		return b.Put([]byte(address), val)
	})
	if err != nil {
		return nil, err
	}
	return &CreateAccountResult{Address: address, Password: password, UUID: uuid}, nil
}

// GetAccount loads an account by address. The lookup lowercases the address
// first: account keys are normalized to lowercase at creation, and this also
// lets a caller who still sends mixed case (an old client, or pre-fix data)
// find their record instead of silently 404ing into a duplicate.
func (s *Store) GetAccount(address string) (*Account, error) {
	var acc Account
	err := s.db.View(func(tx *bolt.Tx) error {
		val := tx.Bucket(bAccounts).Get([]byte(strings.ToLower(address)))
		if val == nil {
			return ErrAccountNotFound
		}
		return json.Unmarshal(val, &acc)
	})
	if err != nil {
		return nil, err
	}
	return &acc, nil
}

// VerifyPassword checks that address/password is a valid credential pair.
// Returns ErrAccountNotFound if the address does not exist, ErrAccountDisabled
// if the account is disabled, nil on success, or a non-nil error for a wrong
// password.
func (s *Store) VerifyPassword(address, password string) error {
	acc, err := s.GetAccount(address)
	if err != nil {
		return err
	}
	if acc.Disabled {
		return ErrAccountDisabled
	}
	if err := bcrypt.CompareHashAndPassword(acc.PasswordHash, []byte(password)); err != nil {
		return fmt.Errorf("wrong password")
	}
	return nil
}

// ListAccounts returns every account (used by the admin). Admin accounts are
// included. Enabled accounts sort before disabled ones (stable within each
// group by address), so disabled accounts appear at the bottom of the panel.
func (s *Store) ListAccounts() ([]Account, error) {
	var out []Account
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bAccounts).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var acc Account
			if err := json.Unmarshal(v, &acc); err != nil {
				continue
			}
			out = append(out, acc)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Stable sort: enabled first, then disabled; within each group by address.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Disabled != out[j].Disabled {
			return !out[i].Disabled // enabled (false) sorts before disabled (true)
		}
		return out[i].Address < out[j].Address
	})
	return out, nil
}

// ResetPassword overwrites an account's password hash with one derived from
// newPassword. It does NOT verify the old password — the caller (admin
// endpoint) has already authenticated. Used by the admin reset-password flow.
// Returns ErrAccountNotFound if the account does not exist.
func (s *Store) ResetPassword(address, newPassword string) error {
	if newPassword == "" {
		return fmt.Errorf("empty password")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bAccounts)
		val := b.Get([]byte(address))
		if val == nil {
			return ErrAccountNotFound
		}
		var acc Account
		if err := json.Unmarshal(val, &acc); err != nil {
			return err
		}
		acc.PasswordHash = hash
		newVal, err := json.Marshal(acc)
		if err != nil {
			return err
		}
		return b.Put([]byte(address), newVal)
	})
}

// CaseNormResult reports what NormalizeAccountCase did: how many account
// keys were already lowercase (untouched), how many were rewritten to their
// lowercase form (renamed), and how many were deleted as duplicates of an
// already-lowercase record (the lowercase twin wins — it is the one future
// logins resolve to).
type CaseNormResult struct {
	AlreadyLower int `json:"already_lower"`
	Renamed      int `json:"renamed"`
	DeletedDupes int `json:"deleted_duplicates"`
}

// NormalizeAccountCase repairs pre-fix account keys that were stored with
// uppercase letters: each key not already lowercase is rewritten to its
// lowercase form. When the lowercase key already exists (the double-listing
// an outside user reported), the uppercase record is dropped as a
// duplicate and the lowercase record is kept (it is what GetAccount now
// resolves to). One transaction; on conflict the lowercase record's
// password/visibility/signature/prefs are the ones that survive. Safe to
// run repeatedly — a clean store is a no-op.
func (s *Store) NormalizeAccountCase() (CaseNormResult, error) {
	var res CaseNormResult
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bAccounts)
		// First pass: collect the keys that need work (cursor mutation
		// during iteration is risky in bbolt).
		type pending struct {
			oldKey, lowerKey []byte
			val              []byte
		}
		var todo []pending
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			lk := bytes.ToLower(k)
			if bytes.Equal(k, lk) {
				res.AlreadyLower++
				continue
			}
			todo = append(todo, pending{oldKey: cloneBytes(k), lowerKey: cloneBytes(lk), val: cloneBytes(v)})
		}
		for _, p := range todo {
			if b.Get(p.lowerKey) != nil {
				// Lowercase twin exists — keep it, drop the uppercase dupe.
				if err := b.Delete(p.oldKey); err != nil {
					return err
				}
				res.DeletedDupes++
				continue
			}
			// Rewrite under the lowercase key, fix the embedded Address
			// field, drop the old key.
			var acc Account
			if err := json.Unmarshal(p.val, &acc); err == nil && acc.Address != string(p.lowerKey) {
				acc.Address = string(p.lowerKey)
				if nv, err := json.Marshal(acc); err == nil {
					p.val = nv
				}
			}
			if err := b.Put(p.lowerKey, p.val); err != nil {
				return err
			}
			if err := b.Delete(p.oldKey); err != nil {
				return err
			}
			res.Renamed++
		}
		return nil
	})
	return res, err
}

// cloneBytes copies a bbolt-owned byte slice (bucket values are only valid
// for the transaction lifetime). bytes.Clone is unavailable before 1.20.
func cloneBytes(b []byte) []byte {
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

// ErrWrongPassword is returned by ChangePassword when the old password does not
// match the stored hash.
var ErrWrongPassword = errors.New("wrong password")

// ChangePassword verifies oldPassword against the stored hash and, on success,
// replaces it with one derived from newPassword. Returns ErrWrongPassword if
// the old password is incorrect, or ErrAccountNotFound if the account is gone.
// newPassword length is enforced (>= MinPasswordLength); empty is rejected.
const MinPasswordLength = 8

func (s *Store) ChangePassword(address, oldPassword, newPassword string) error {
	if len(newPassword) < MinPasswordLength {
		return fmt.Errorf("new password must be at least %d characters", MinPasswordLength)
	}
	// Verify the old password first (do this outside the write tx so we don't
	// hold a write lock for a bcrypt compare).
	if err := s.VerifyPassword(address, oldPassword); err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			return ErrAccountNotFound
		}
		return ErrWrongPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bAccounts)
		val := b.Get([]byte(address))
		if val == nil {
			return ErrAccountNotFound
		}
		var acc Account
		if err := json.Unmarshal(val, &acc); err != nil {
			return err
		}
		acc.PasswordHash = hash
		newVal, err := json.Marshal(acc)
		if err != nil {
			return err
		}
		return b.Put([]byte(address), newVal)
	})
}

// SetAccountDisabled toggles an account's disabled flag. A disabled account
// cannot authenticate (VerifyPassword returns ErrAccountDisabled), so it can
// neither send nor read mail, but the account and its message history persist.
// Reversible via SetAccountDisabled(address, false).
func (s *Store) SetAccountDisabled(address string, disabled bool) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bAccounts)
		val := b.Get([]byte(address))
		if val == nil {
			return ErrAccountNotFound
		}
		var acc Account
		if err := json.Unmarshal(val, &acc); err != nil {
			return err
		}
		acc.Disabled = disabled
		newVal, err := json.Marshal(acc)
		if err != nil {
			return err
		}
		return b.Put([]byte(address), newVal)
	})
}

// UpdateProfile sets an account's directory visibility and signature. The
// caller is responsible for trimming/length-limiting signature. Returns
// ErrAccountNotFound if the account does not exist.
func (s *Store) UpdateProfile(address string, visible bool, signature string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bAccounts)
		val := b.Get([]byte(address))
		if val == nil {
			return ErrAccountNotFound
		}
		var acc Account
		if err := json.Unmarshal(val, &acc); err != nil {
			return err
		}
		acc.Visible = visible
		acc.Signature = signature
		newVal, err := json.Marshal(acc)
		if err != nil {
			return err
		}
		return b.Put([]byte(address), newVal)
	})
}

// UpdatePrefs merges the given preference keys into the account's stored
// Prefs map (existing keys not mentioned are kept; a nil value for a key
// removes it). The API layer whitelist-validates keys and value types;
// the store just persists the map.
func (s *Store) UpdatePrefs(address string, prefs map[string]any) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bAccounts)
		val := b.Get([]byte(address))
		if val == nil {
			return ErrAccountNotFound
		}
		var acc Account
		if err := json.Unmarshal(val, &acc); err != nil {
			return err
		}
		if acc.Prefs == nil {
			acc.Prefs = map[string]any{}
		}
		for k, v := range prefs {
			if v == nil {
				delete(acc.Prefs, k)
				continue
			}
			acc.Prefs[k] = v
		}
		if len(acc.Prefs) == 0 {
			acc.Prefs = nil // keep old records byte-identical when empty
		}
		newVal, err := json.Marshal(acc)
		if err != nil {
			return err
		}
		return b.Put([]byte(address), newVal)
	})
}

// ListVisibleAccounts returns every account with Visible == true, sorted by
// address. Disabled accounts are excluded even if marked visible (a disabled
// account should not advertise itself). Used to build the public directory.
func (s *Store) ListVisibleAccounts() ([]Account, error) {
	var out []Account
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bAccounts).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var acc Account
			if err := json.Unmarshal(v, &acc); err != nil {
				continue
			}
			if acc.Visible && !acc.Disabled {
				out = append(out, acc)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Address < out[j].Address
	})
	return out, nil
}

// GeneratePassword returns a cryptographically random password of length n
// from the URL-safe alphabet. Exported so the admin handler can generate a
// random password when the caller does not supply one.
func GeneratePassword(n int) string { return generatePassword(n) }

// EnsureAdmin creates the admin account if it does not already exist. Called
// at server startup from config.
func (s *Store) EnsureAdmin(address, password string) error {
	// The config file's casing is the caller's choice, not a contract: keys
	// are always lowercase (the last non-normalizing bucket write path).
	address = strings.ToLower(address)
	if _, err := s.GetAccount(address); err == nil {
		return nil // already exists
	} else if !errors.Is(err, ErrAccountNotFound) {
		return err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash admin password: %w", err)
	}
	acc := Account{
		UUID:         hexID(),
		Address:      address,
		PasswordHash: hash,
		IsAdmin:      true,
		CreatedAt:    s.now().Unix(),
	}
	val, err := json.Marshal(acc)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bAccounts).Put([]byte(address), val)
	})
}

// BootstrapSystem initializes a fresh installation: creates the admin account
// with the given password, stores the domain, and marks the system initialized.
// This is called once (either from the setup wizard or as config migration).
// If already initialized it returns nil (idempotent).
//
// adminLocalPart is the local-part of the admin address (e.g. "admin"); the
// full address becomes adminLocalPart + "@" + domain.
func (s *Store) BootstrapSystem(adminLocalPart, adminPassword, domain string) error {
	if s.IsInitialized() {
		return nil // already bootstrapped; idempotent
	}
	adminAddress := adminLocalPart + "@" + domain
	if err := s.EnsureAdmin(adminAddress, adminPassword); err != nil {
		return fmt.Errorf("create admin: %w", err)
	}
	if err := s.SetDomain(domain); err != nil {
		return fmt.Errorf("set domain: %w", err)
	}
	return s.SetInitialized()
}

// generatePassword returns a cryptographically random password from the
// URL-safe alphabet.
func generatePassword(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic("store: crypto/rand failed during password generation: " + err.Error())
	}
	for i, b := range buf {
		buf[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(buf)
}

// TeamMember is one provisioned account in a team registration.
type TeamMember struct {
	Address  string `json:"address"`
	Password string `json:"password"`
}

// RegisterTeam provisions an owner account plus teamSize subordinate
// member accounts and their declare edges in ONE transaction (crash =
// nothing half-created). team_size counts MEMBERS ONLY — the owner is
// MaxSubordinates caps how many subordinate accounts one owner may have,
// whether provisioned in one shot via team register or added pairwise via
// the declare endpoint. Single source of truth for the store validation,
// the API validation, and the declare guard (superior raised it 10 -> 20
// for the 0.2.2 batch).
const MaxSubordinates = 20

// extra (architect ruling: default 3 = 1 owner + 3 members; bounded by
// MaxSubordinates).
//
// memberNames is the v2 contract: when non-empty (len == teamSize) those
// caller-chosen local-parts are used as the member names. A name that
// collides with an existing account (or another member in this batch) is
// serially de-duplicated with a -2/--3… suffix, and after a few tries a
// random bot-<8> name is the final fallback. When memberNames is empty
// (the v1 path), members are named bot-<8random> as before. The owner
// uses the caller-supplied password; member passwords are generated. The
// returned members carry the ACTUAL addresses (post-dedup) so the caller
// can show them verbatim.
func (s *Store) RegisterTeam(username, domain, password string, teamSize int, memberNames []string) (*TeamMember, *[]TeamMember, error) {
	if teamSize < 1 || teamSize > MaxSubordinates {
		return nil, nil, fmt.Errorf("team_size must be 1-%d", MaxSubordinates)
	}
	// v2: a member name list, when supplied, must match team_size. The
	// handler validates shape/charset before calling; here we only enforce
	// the count contract so the store is safe to call directly.
	if len(memberNames) > 0 && len(memberNames) != teamSize {
		return nil, nil, fmt.Errorf("members length %d != team_size %d", len(memberNames), teamSize)
	}
	ownerAddr := strings.ToLower(username + "@" + domain)
	ownerHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, nil, fmt.Errorf("hash password: %w", err)
	}
	now := s.now().Unix()
	owner := TeamMember{Address: ownerAddr, Password: password}
	var members []TeamMember

	err = s.db.Update(func(tx *bolt.Tx) error {
		ab := tx.Bucket(bAccounts)
		sb := tx.Bucket(bSubs)
		if ab.Get([]byte(ownerAddr)) != nil {
			return ErrAccountExists
		}
		acc := Account{UUID: hexID(), Address: ownerAddr, PasswordHash: ownerHash, CreatedAt: now}
		if u := strings.TrimSpace(username); u != "" && u != strings.ToLower(u) {
			acc.DisplayLocal = u // preserve the owner's own casing (V06)
		}
		val, err := json.Marshal(acc)
		if err != nil {
			return err
		}
		if err := ab.Put([]byte(ownerAddr), val); err != nil {
			return err
		}
		// allocated tracks local-parts already claimed in THIS tx so two
		// members can't collide with each other even before their keys land.
		allocated := map[string]bool{ownerAddr: true}
		// allocateMemberName resolves a non-conflicting lowercase local-part
		// for the i-th member: v2 uses the caller's name (serial -2/-3…
		// suffix on collision, then a bot-<8> fallback); v1 (no list) uses
		// bot-<8> directly.
		allocateMemberName := func(i int) (string, error) {
			base := ""
			if i < len(memberNames) {
				base = strings.ToLower(strings.TrimSpace(memberNames[i]))
			}
			if base == "" {
				// v1 path, or a v2 caller that sent an empty slot.
				for attempt := 0; attempt < 5; attempt++ {
					cand := "bot-" + generatePassword(8)
					addr := cand + "@" + domain
					if !allocated[addr] && ab.Get([]byte(addr)) == nil {
						allocated[addr] = true
						return cand, nil
					}
				}
				return "", fmt.Errorf("could not allocate member name")
			}
			// v2 path: try the base, then base-2, base-3… up to 20, then
			// fall back to a random bot- name so the registration never
			// fails purely on naming congestion.
			cand := base
			for attempt := 0; ; attempt++ {
				addr := cand + "@" + domain
				if !allocated[addr] && ab.Get([]byte(addr)) == nil {
					allocated[addr] = true
					return cand, nil
				}
				if attempt >= 20 {
					break // fall through to random fallback
				}
				cand = fmt.Sprintf("%s-%d", base, attempt+2)
			}
			for attempt := 0; attempt < 5; attempt++ {
				cand = "bot-" + generatePassword(8)
				addr := cand + "@" + domain
				if !allocated[addr] && ab.Get([]byte(addr)) == nil {
					allocated[addr] = true
					return cand, nil
				}
			}
			return "", fmt.Errorf("could not allocate member name (base %q exhausted)", base)
		}
		// Members: each declared under the owner.
		for i := 0; i < teamSize; i++ {
			name, err := allocateMemberName(i)
			if err != nil {
				return err
			}
			pw := generatePassword(24)
			hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
			if err != nil {
				return err
			}
			mAddr := name + "@" + domain
			mAcc := Account{UUID: hexID(), Address: mAddr, PasswordHash: hash, CreatedAt: now}
			if i < len(memberNames) {
				if orig := strings.TrimSpace(memberNames[i]); orig != "" && orig != strings.ToLower(orig) && name == strings.ToLower(orig) {
					mAcc.DisplayLocal = orig // un-collided member keeps own casing (V06)
				}
			}
			mVal, err := json.Marshal(mAcc)
			if err != nil {
				return err
			}
			if err := ab.Put([]byte(mAddr), mVal); err != nil {
				return err
			}
			rec, _ := json.Marshal(SubRecord{Scope: "both", CreatedAt: now})
			if err := sb.Put(subKey(ownerAddr, mAddr), rec); err != nil {
				return err
			}
			members = append(members, TeamMember{Address: mAddr, Password: pw})
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return &owner, &members, nil
}
