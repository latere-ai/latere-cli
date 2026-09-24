// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package modelkey

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/latere-ai/latere-cli/internal/api"
)

// fakeAuth records the key requests and revocations the CLI makes.
type fakeAuth struct {
	created  []api.KeyRequest
	revoked  []string
	status   string
	createEr error
	n        int
}

func (f *fakeAuth) auth() Auth {
	return Auth{
		Create: func(_ context.Context, authBase, access string, r api.KeyRequest) (api.CreatedKey, error) {
			if f.createEr != nil {
				return api.CreatedKey{}, f.createEr
			}
			f.created = append(f.created, r)
			f.n++
			status := f.status
			if status == "" {
				status = "active"
			}
			id := "key-" + string(rune('0'+f.n))
			return api.CreatedKey{ID: id, Prefix: "pat_" + id, Key: "pat_" + id + ".secret", Status: status}, nil
		},
		Revoke: func(_ context.Context, authBase, access, id string) error {
			f.revoked = append(f.revoked, id)
			return nil
		},
	}
}

func testKeys(t *testing.T, st Store, fa *fakeAuth, now time.Time) *Keys {
	t.Helper()
	t.Setenv(EnvKey, "")
	return &Keys{Store: st, Auth: fa.auth(), Now: func() time.Time { return now }, Host: "laptop"}
}

func fileStore(t *testing.T) File {
	t.Helper()
	return File{Path: filepath.Join(t.TempDir(), "model-keys.json")}
}

var login = Login{AuthBase: "https://auth.example", Access: "login", Sub: "u1"}

// TestEnsureCreatesOnFirstUse is criterion 1 and 2 at the package: the first
// use creates a key with the grant's lifetime and the host's name, keeps it,
// and the next use answers the same key; the personal context sends no org,
// and another context has its own key.
func TestEnsureCreatesOnFirstUse(t *testing.T) {
	fa := &fakeAuth{}
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	k := testKeys(t, fileStore(t), fa, now)

	first, err := k.Ensure(context.Background(), login)
	if err != nil || !first.Created {
		t.Fatalf("first = %+v, %v", first, err)
	}
	if len(fa.created) != 1 || fa.created[0].OrgID != "" || fa.created[0].Name != "latere-cli on laptop" ||
		!fa.created[0].ExpiresAt.Equal(now.Add(Lifetime)) {
		t.Fatalf("request = %+v", fa.created)
	}
	again, err := k.Ensure(context.Background(), login)
	if err != nil || again.Created || again.Record.Value != first.Record.Value {
		t.Fatalf("again = %+v, %v; want the stored key", again, err)
	}

	org := login
	org.OrgID = "org-1"
	inOrg, err := k.Ensure(context.Background(), org)
	if err != nil || !inOrg.Created || inOrg.Record.Value == first.Record.Value || fa.created[1].OrgID != "org-1" {
		t.Fatalf("in the org = %+v, %v; requests %+v", inOrg, err, fa.created)
	}
}

// TestEnsureRenewsNearTheEnd is criterion 7.
func TestEnsureRenewsNearTheEnd(t *testing.T) {
	fa := &fakeAuth{}
	st := fileStore(t)
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	k := testKeys(t, st, fa, now)
	first, err := k.Ensure(context.Background(), login)
	if err != nil {
		t.Fatal(err)
	}
	k.Now = func() time.Time { return first.Record.ExpiresAt.Add(-RenewBefore + time.Hour) }
	next, err := k.Ensure(context.Background(), login)
	if err != nil || !next.Created || next.Record.ID == first.Record.ID {
		t.Fatalf("near the end = %+v, %v; want a new key", next, err)
	}
	if len(fa.revoked) != 1 || fa.revoked[0] != first.Record.ID {
		t.Fatalf("revoked = %v, want the old key", fa.revoked)
	}
}

// TestEnsureExplainsRefusals is criterion 3's package half.
func TestEnsureExplainsRefusals(t *testing.T) {
	for code, want := range map[string]string{
		"member_keys_off": "does not let members create their own keys",
		"not_a_member":    "no longer a member",
		"key_limit":       "maximum number of keys",
	} {
		fa := &fakeAuth{createEr: &api.KeyError{Status: 403, Code: code}}
		k := testKeys(t, fileStore(t), fa, time.Now())
		_, err := k.Ensure(context.Background(), login)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: err = %v, want %q", code, err, want)
		}
	}
}

// TestEnvKeyWins is criterion 5's override.
func TestEnvKeyWins(t *testing.T) {
	fa := &fakeAuth{}
	k := testKeys(t, fileStore(t), fa, time.Now())
	t.Setenv(EnvKey, "pat_ci.value")
	r, err := k.Ensure(context.Background(), login)
	if err != nil || !r.FromEnv || r.Record.Value != "pat_ci.value" || len(fa.created) != 0 {
		t.Fatalf("env = %+v, %v, created %v", r, err, fa.created)
	}
}

// TestKeychainIsUsedWhenAvailable is criterion 5: with a keychain, the key
// is kept there and not in the file.
func TestKeychainIsUsedWhenAvailable(t *testing.T) {
	keyring.MockInit()
	file := fileStore(t)
	st := &Fallback{Primary: Keychain{}, Secondary: file}
	k := testKeys(t, st, &fakeAuth{}, time.Now())
	if _, err := k.Ensure(context.Background(), login); err != nil {
		t.Fatal(err)
	}
	if st.Name() != "system keychain" {
		t.Fatalf("stored in %s, want the keychain", st.Name())
	}
	if _, err := os.Stat(file.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the file was written although the keychain works: %v", err)
	}
	slots, err := Keychain{}.Slots()
	if err != nil || len(slots) != 1 || slots[0] != login.Slot() {
		t.Fatalf("keychain slots = %v, %v", slots, err)
	}
}

// TestFileFallbackWhenNoKeychain is criterion 5's fallback: the key lands in
// a 0600 file.
func TestFileFallbackWhenNoKeychain(t *testing.T) {
	keyring.MockInitWithError(errors.New("no secret service"))
	file := fileStore(t)
	st := &Fallback{Primary: Keychain{}, Secondary: file}
	k := testKeys(t, st, &fakeAuth{}, time.Now())
	r, err := k.Ensure(context.Background(), login)
	if err != nil {
		t.Fatal(err)
	}
	if st.Name() != file.Path {
		t.Fatalf("stored in %s, want the file", st.Name())
	}
	info, err := os.Stat(file.Path)
	if err != nil {
		t.Fatal(err)
	}
	if runtimePerm := info.Mode().Perm(); runtimePerm != 0o600 {
		t.Fatalf("file mode = %v, want 0600", runtimePerm)
	}
	got, ok, err := st.Get(login.Slot())
	if err != nil || !ok || got.Value != r.Record.Value {
		t.Fatalf("read back = %+v %v %v", got, ok, err)
	}
}

// TestForgetAllRevokesTheLoginsKeys is criterion 8's package half: logout
// revokes and forgets every context's key of the login, and no other
// login's.
func TestForgetAllRevokesTheLoginsKeys(t *testing.T) {
	fa := &fakeAuth{}
	st := fileStore(t)
	k := testKeys(t, st, fa, time.Now())
	org := login
	org.OrgID = "org-1"
	other := login
	other.Sub = "u2"
	for _, l := range []Login{login, org, other} {
		if _, err := k.Ensure(context.Background(), l); err != nil {
			t.Fatal(err)
		}
	}
	if errs := k.ForgetAll(context.Background(), login.AuthBase, login.Sub, login.Access); len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	if len(fa.revoked) != 2 {
		t.Fatalf("revoked = %v, want the two keys of u1", fa.revoked)
	}
	slots, err := st.Slots()
	if err != nil || len(slots) != 1 || slots[0] != other.Slot() {
		t.Fatalf("left = %v, %v; want u2's key alone", slots, err)
	}
}

// TestFresh names the window in which the core may not know a key yet.
func TestFresh(t *testing.T) {
	now := time.Now()
	k := &Keys{Now: func() time.Time { return now }}
	if !k.Fresh(Record{CreatedAt: now.Add(-time.Minute)}) || k.Fresh(Record{CreatedAt: now.Add(-FreshFor - time.Second)}) || k.Fresh(Record{}) {
		t.Fatal("Fresh is wrong at its edges")
	}
}
