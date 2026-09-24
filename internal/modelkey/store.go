// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Package modelkey keeps the key the CLI presents to the Lux core's model
// endpoints (specs/006-model-key.md): created at auth on first use, one per
// login and context, kept in the system keychain, or beside the login when
// the machine has no keychain.
package modelkey

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/zalando/go-keyring"
	"latere.ai/x/pkg/atomicfile"

	"github.com/latere-ai/latere-cli/internal/config"
)

// Record is one stored key.
type Record struct {
	ID     string `json:"id"`
	Prefix string `json:"prefix"`
	Value  string `json:"value"`
	// AuthBase, Sub and Context name the login and context the key was
	// created in; Context is an organization id or "personal".
	AuthBase  string    `json:"auth_base"`
	Sub       string    `json:"sub"`
	Context   string    `json:"context"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// PersonalContext is the Context of a key created with no organization.
const PersonalContext = "personal"

// Slot names where one login's key for one context is kept.
func Slot(authBase, sub, context string) string {
	return authBase + "|" + sub + "|" + context
}

// Store keeps records by slot.
type Store interface {
	// Get answers the record in slot, and false when there is none.
	Get(slot string) (Record, bool, error)
	Put(slot string, r Record) error
	// Delete forgets slot; forgetting what is not there is not an error.
	Delete(slot string) error
	// Slots lists every slot held, for logout.
	Slots() ([]string, error)
	// Name says where records are kept, for `models key`.
	Name() string
}

// keychainService is the keychain entry's service; the account is the slot.
const keychainService = "latere-cli model key"

// keychainIndexAccount is the entry that lists the slots the keychain
// holds, because a keychain cannot be enumerated portably.
const keychainIndexAccount = "latere-cli model key slots"

// Keychain keeps records in the system keychain.
type Keychain struct{}

// Name is "system keychain".
func (Keychain) Name() string { return "system keychain" }

// Get reads slot's entry.
func (Keychain) Get(slot string) (Record, bool, error) {
	secret, err := keyring.Get(keychainService, slot)
	if errors.Is(err, keyring.ErrNotFound) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("keychain: %w", err)
	}
	var r Record
	if err := json.Unmarshal([]byte(secret), &r); err != nil {
		return Record{}, false, fmt.Errorf("keychain entry for %s: %w", slot, err)
	}
	return r, true, nil
}

// Put writes slot's entry and records the slot in the index.
func (k Keychain) Put(slot string, r Record) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := keyring.Set(keychainService, slot, string(b)); err != nil {
		return fmt.Errorf("keychain: %w", err)
	}
	slots, err := k.Slots()
	if err != nil {
		return err
	}
	if slices.Contains(slots, slot) {
		return nil
	}
	return k.writeIndex(append(slots, slot))
}

// Delete removes slot's entry and its index line.
func (k Keychain) Delete(slot string) error {
	if err := keyring.Delete(keychainService, slot); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("keychain: %w", err)
	}
	slots, err := k.Slots()
	if err != nil {
		return err
	}
	kept := slots[:0]
	for _, s := range slots {
		if s != slot {
			kept = append(kept, s)
		}
	}
	return k.writeIndex(kept)
}

// Slots reads the index.
func (Keychain) Slots() ([]string, error) {
	raw, err := keyring.Get(keychainService, keychainIndexAccount)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("keychain: %w", err)
	}
	var slots []string
	if err := json.Unmarshal([]byte(raw), &slots); err != nil {
		return nil, fmt.Errorf("keychain index: %w", err)
	}
	return slots, nil
}

func (Keychain) writeIndex(slots []string) error {
	b, err := json.Marshal(slots)
	if err != nil {
		return err
	}
	if err := keyring.Set(keychainService, keychainIndexAccount, string(b)); err != nil {
		return fmt.Errorf("keychain: %w", err)
	}
	return nil
}

// File keeps records in a 0600 JSON file beside the login.
type File struct{ Path string }

// DefaultFile is ~/.config/latere/model-keys.json, or LATERE_MODEL_KEYS_FILE.
func DefaultFile() File {
	if v := os.Getenv("LATERE_MODEL_KEYS_FILE"); v != "" {
		return File{Path: v}
	}
	return File{Path: config.Path("model-keys.json")}
}

// Name is the file's path.
func (f File) Name() string { return f.Path }

func (f File) read() (map[string]Record, error) {
	b, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Record{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]Record{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", f.Path, err)
	}
	return m, nil
}

func (f File) write(m map[string]Record) error {
	if f.Path == "" {
		return errors.New("cannot determine the model key file")
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteSync(f.Path, b, 0o600)
}

// Get reads slot.
func (f File) Get(slot string) (Record, bool, error) {
	m, err := f.read()
	if err != nil {
		return Record{}, false, err
	}
	r, ok := m[slot]
	return r, ok, nil
}

// Put writes slot.
func (f File) Put(slot string, r Record) error {
	m, err := f.read()
	if err != nil {
		return err
	}
	m[slot] = r
	return f.write(m)
}

// Delete forgets slot.
func (f File) Delete(slot string) error {
	m, err := f.read()
	if err != nil {
		return err
	}
	if _, ok := m[slot]; !ok {
		return nil
	}
	delete(m, slot)
	return f.write(m)
}

// Slots lists the file's slots.
func (f File) Slots() ([]string, error) {
	m, err := f.read()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(m))
	for s := range m {
		out = append(out, s)
	}
	return out, nil
}

// Fallback keeps records in Primary, the keychain, and in Secondary, the
// file, when Primary answers that it cannot: a headless host, a container or
// a CI runner has no keychain, and the CLI runs there too. Reads look in
// both, so a key the file kept is found after a keychain appears.
type Fallback struct {
	Primary, Secondary Store
	// used names the store the last write landed in.
	used string
}

// Open is the store the CLI uses: the keychain, falling back to the file.
func Open() *Fallback {
	return &Fallback{Primary: Keychain{}, Secondary: DefaultFile()}
}

// Name is where the last write landed, else the keychain.
func (f *Fallback) Name() string {
	if f.used != "" {
		return f.used
	}
	return f.Primary.Name()
}

// Get looks in the keychain, then the file.
func (f *Fallback) Get(slot string) (Record, bool, error) {
	if r, ok, err := f.Primary.Get(slot); err == nil && ok {
		f.used = f.Primary.Name()
		return r, true, nil
	}
	r, ok, err := f.Secondary.Get(slot)
	if ok {
		f.used = f.Secondary.Name()
	}
	return r, ok, err
}

// Put writes to the keychain, or to the file when the keychain cannot.
func (f *Fallback) Put(slot string, r Record) error {
	if err := f.Primary.Put(slot, r); err == nil {
		f.used = f.Primary.Name()
		return nil
	}
	if err := f.Secondary.Put(slot, r); err != nil {
		return err
	}
	f.used = f.Secondary.Name()
	return nil
}

// Delete forgets slot in both. A keychain that cannot be reached holds
// nothing this CLI wrote there, so its error counts only when the entry is
// still readable afterwards.
func (f *Fallback) Delete(slot string) error {
	if err := f.Primary.Delete(slot); err != nil {
		if _, still, gerr := f.Primary.Get(slot); gerr == nil && still {
			return err
		}
	}
	return f.Secondary.Delete(slot)
}

// Slots lists both.
func (f *Fallback) Slots() ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, st := range []Store{f.Primary, f.Secondary} {
		slots, err := st.Slots()
		if err != nil {
			continue
		}
		for _, s := range slots {
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	return out, nil
}
