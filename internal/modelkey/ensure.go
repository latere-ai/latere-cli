// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package modelkey

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/latere-ai/latere-cli/internal/api"
)

const (
	// Lifetime is how long a key the CLI creates lives (decision D1).
	Lifetime = 90 * 24 * time.Hour
	// RenewBefore is how close to its end a key is replaced before use.
	RenewBefore = 7 * 24 * time.Hour
	// FreshFor is how long after its creation a key may still be unknown
	// to the core, while platformd registers it from auth's event.
	FreshFor = 2 * time.Minute
	// EnvKey is the environment variable that hands the CLI a key, and
	// wins over every store.
	EnvKey = "LATERE_MODEL_KEY"
)

// Login is the signed-in person the key is for.
type Login struct {
	AuthBase string
	// Access is the login's access token, what auth's /me/keys takes.
	Access string
	Sub    string
	// OrgID is the login's current context, "" for personal.
	OrgID string
}

// Context is the stored context name of the login.
func (l Login) Context() string {
	if l.OrgID == "" {
		return PersonalContext
	}
	return l.OrgID
}

// Slot is where the login's key for its context is kept.
func (l Login) Slot() string { return Slot(l.AuthBase, l.Sub, l.Context()) }

// Auth is what the keys are created and revoked with: api.CreateModelKey
// and api.RevokeKey, replaced in tests.
type Auth struct {
	Create func(ctx context.Context, authBase, access string, r api.KeyRequest) (api.CreatedKey, error)
	Revoke func(ctx context.Context, authBase, access, id string) error
}

// DefaultAuth calls auth.
var DefaultAuth = Auth{Create: api.CreateModelKey, Revoke: api.RevokeKey}

// Keys answers the key for a login, creating it on first use.
type Keys struct {
	Store Store
	Auth  Auth
	Now   func() time.Time
	// Host is the machine name in the key's name.
	Host string
}

// New is Keys over the CLI's store and auth.
func New() *Keys {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "this machine"
	}
	return &Keys{Store: Open(), Auth: DefaultAuth, Now: time.Now, Host: host}
}

// Result is the key a call presents, and whether it was created by this
// call, which is when the core may not know it yet.
type Result struct {
	Record  Record
	Created bool
	// FromEnv is a key handed in through EnvKey, which the CLI neither
	// created nor stores.
	FromEnv bool
}

// Fresh reports whether the key is young enough that the core may not
// know it yet.
func (k *Keys) Fresh(r Record) bool {
	return !r.CreatedAt.IsZero() && k.Now().Sub(r.CreatedAt) < FreshFor
}

// Ensure answers the login's key for its context: the one handed in through
// EnvKey, the stored one while it is far from its end, or a new one. A key
// near its end is revoked at auth and replaced.
func (k *Keys) Ensure(ctx context.Context, l Login) (Result, error) {
	if v := strings.TrimSpace(os.Getenv(EnvKey)); v != "" {
		return Result{Record: Record{Value: v, Context: l.Context()}, FromEnv: true}, nil
	}
	r, ok, err := k.Store.Get(l.Slot())
	if err != nil {
		return Result{}, fmt.Errorf("read the model key: %w", err)
	}
	if ok && (r.ExpiresAt.IsZero() || k.Now().Before(r.ExpiresAt.Add(-RenewBefore))) {
		return Result{Record: r}, nil
	}
	if ok {
		return k.Replace(ctx, l, r)
	}
	return k.create(ctx, l)
}

// Replace revokes old at auth, forgets it, and creates the login's next
// key. A revocation auth does not answer does not stop the replacement: the
// old key is forgotten here either way.
func (k *Keys) Replace(ctx context.Context, l Login, old Record) (Result, error) {
	if old.ID != "" {
		if err := k.Auth.Revoke(ctx, l.AuthBase, l.Access, old.ID); err != nil {
			fmt.Fprintf(os.Stderr, "note: could not revoke the previous model key %s: %v\n", old.Prefix, err)
		}
	}
	if err := k.Store.Delete(l.Slot()); err != nil {
		return Result{}, fmt.Errorf("forget the previous model key: %w", err)
	}
	return k.create(ctx, l)
}

func (k *Keys) create(ctx context.Context, l Login) (Result, error) {
	now := k.Now().UTC()
	created, err := k.Auth.Create(ctx, l.AuthBase, l.Access, api.KeyRequest{
		Name:      "latere-cli on " + k.Host,
		OrgID:     l.OrgID,
		ExpiresAt: now.Add(Lifetime),
	})
	if err != nil {
		return Result{}, explain(err)
	}
	r := Record{
		ID: created.ID, Prefix: created.Prefix, Value: created.Key,
		AuthBase: l.AuthBase, Sub: l.Sub, Context: l.Context(),
		Status: created.Status, CreatedAt: now, ExpiresAt: now.Add(Lifetime),
	}
	if created.ExpiresAt != nil {
		r.ExpiresAt = created.ExpiresAt.UTC()
	}
	if err := k.Store.Put(l.Slot(), r); err != nil {
		// The key exists at auth and nowhere here: revoke it rather than
		// leave a live key nobody holds.
		if rerr := k.Auth.Revoke(ctx, l.AuthBase, l.Access, r.ID); rerr != nil {
			return Result{}, fmt.Errorf("store the model key: %w (and revoking it failed: %w)", err, rerr)
		}
		return Result{}, fmt.Errorf("store the model key: %w", err)
	}
	return Result{Record: r, Created: true}, nil
}

// Forget revokes the login's key for its context at auth and forgets it.
// It answers the record it forgot, and false when there was none.
func (k *Keys) Forget(ctx context.Context, l Login) (Record, bool, error) {
	r, ok, err := k.Store.Get(l.Slot())
	if err != nil || !ok {
		return Record{}, false, err
	}
	if err := k.Auth.Revoke(ctx, l.AuthBase, l.Access, r.ID); err != nil {
		return r, true, fmt.Errorf("revoke the model key: %w", err)
	}
	if err := k.Store.Delete(l.Slot()); err != nil {
		return r, true, fmt.Errorf("forget the model key: %w", err)
	}
	return r, true, nil
}

// ForgetAll revokes and forgets every key of the login sub at authBase, in
// every context, for logout. Each failure is reported and the rest still
// go; the local copies are forgotten whatever auth answers.
func (k *Keys) ForgetAll(ctx context.Context, authBase, sub, access string) []error {
	slots, err := k.Store.Slots()
	if err != nil {
		return []error{err}
	}
	var errs []error
	prefix := authBase + "|" + sub + "|"
	for _, slot := range slots {
		if !strings.HasPrefix(slot, prefix) {
			continue
		}
		r, ok, err := k.Store.Get(slot)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if ok && r.ID != "" && access != "" {
			if err := k.Auth.Revoke(ctx, authBase, access, r.ID); err != nil {
				errs = append(errs, fmt.Errorf("revoke model key %s: %w", r.Prefix, err))
			}
		}
		if err := k.Store.Delete(slot); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// explain turns auth's refusals into the sentence the person acts on.
func explain(err error) error {
	var ke *api.KeyError
	if !errors.As(err, &ke) {
		return fmt.Errorf("create a model key: %w", err)
	}
	switch ke.Code {
	case "member_keys_off":
		return errors.New("this organization does not let members create their own keys; ask an org admin, or switch to your personal context with `latere org --personal`")
	case "not_a_member":
		return errors.New("you are no longer a member of this organization; switch context with `latere org`")
	case "key_limit":
		return errors.New("you already hold the maximum number of keys; revoke one in the console, then retry")
	}
	return fmt.Errorf("create a model key: %w", err)
}
