// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/latere-ai/latere-cli/internal/api"
	"github.com/latere-ai/latere-cli/internal/modelkey"
)

// The model key (specs/006-model-key.md). The Lux core behind the origin
// matches the bearer of a model call by the SHA-256 of a key it was told
// about. So every model call presents a key: created at auth on first use,
// one per login and context, kept in the system keychain.

// newModelKeys is the key source; tests replace it.
var newModelKeys = modelkey.New

// retryWindow is how long a model call retries a fresh key the core does
// not know yet, while platformd registers it from auth's event.
var retryWindow = 30 * time.Second

// retryFirstWait is the first wait of that retry; each next one doubles.
var retryFirstWait = time.Second

// modelKeyLogin is the saved login as the key source needs it: the issuer,
// the access token auth's /me/keys takes, the subject and the context.
func modelKeyLogin(ctx context.Context, modelsURL, authURL string) (modelkey.Login, error) {
	access, authBase, err := api.LoginToken(ctx, api.ResolveAuthURL(resolveModelsURL(modelsURL), authURL))
	if err != nil {
		return modelkey.Login{}, err
	}
	info, err := principalFromJWT(access)
	if err != nil {
		return modelkey.Login{}, fmt.Errorf("read the saved login: %w; run `latere login`", err)
	}
	return modelkey.Login{AuthBase: authBase, Access: access, Sub: info.Sub, OrgID: info.OrgID}, nil
}

// modelKey answers the key the next model call presents: the one handed in
// through LATERE_MODEL_KEY, which needs no login, or the current login's
// key for its context, created on first use.
func modelKey(ctx context.Context, modelsURL, authURL string) (*modelkey.Keys, modelkey.Login, modelkey.Result, error) {
	keys := newModelKeys()
	if res, ok := modelkey.FromEnv(); ok {
		return keys, modelkey.Login{}, res, nil
	}
	l, err := modelKeyLogin(ctx, modelsURL, authURL)
	if err != nil {
		return nil, modelkey.Login{}, modelkey.Result{}, err
	}
	res, err := keys.Ensure(ctx, l)
	if err != nil {
		return nil, modelkey.Login{}, modelkey.Result{}, err
	}
	return keys, l, res, nil
}

// modelKeyProvenance is what `models env` says about the key it prints.
func modelKeyProvenance(res modelkey.Result, store string) string {
	if res.FromEnv {
		return "model key from $" + modelkey.EnvKey
	}
	r := res.Record
	s := fmt.Sprintf("model key %s (%s context), kept in %s", r.Prefix, r.Context, store)
	if r.Status == "pending_approval" {
		s += "; an org admin must approve it before it works"
	} else if res.Created {
		s += "; created now, usable within a minute"
	}
	return s
}

// callWithModelKey makes one model call with the key, retrying while the
// core may not know it yet: a fresh key's 401 is retried with a growing wait
// for up to retryWindow; an older key's 401 means it was revoked or lost, so
// it is replaced once and the new key gets the same retry.
func callWithModelKey(ctx context.Context, keys *modelkey.Keys, l modelkey.Login, res modelkey.Result,
	call func(bearer string) ([]byte, error)) ([]byte, error) {
	replaced := false
	for {
		raw, err := callWhileFresh(ctx, keys, res, call)
		var ae *api.APIError
		if err == nil || !errors.As(err, &ae) || ae.Status != http.StatusUnauthorized || res.FromEnv || replaced {
			if err != nil && res.Record.Status == "pending_approval" && errors.As(err, &ae) && ae.Status == http.StatusUnauthorized {
				return nil, errors.New("the model key waits for an org admin's approval; ask one to approve it in the console")
			}
			return raw, err
		}
		fmt.Fprintf(os.Stderr, "note: the model key %s was refused; replacing it\n", res.Record.Prefix)
		next, rerr := keys.Replace(ctx, l, res.Record)
		if rerr != nil {
			return nil, rerr
		}
		res, replaced = next, true
	}
}

// callWhileFresh makes the call, and retries its 401 while the key is young
// enough that the core may not know it yet.
func callWhileFresh(ctx context.Context, keys *modelkey.Keys, res modelkey.Result, call func(bearer string) ([]byte, error)) ([]byte, error) {
	deadline := time.Now().Add(retryWindow)
	wait := retryFirstWait
	for {
		raw, err := call(res.Record.Value)
		var ae *api.APIError
		fresh := res.Created || keys.Fresh(res.Record)
		if err == nil || !errors.As(err, &ae) || ae.Status != http.StatusUnauthorized ||
			!fresh || time.Now().Add(wait).After(deadline) {
			return raw, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		wait *= 2
	}
}

// modelKeySource is the bearer source of a session whose model calls a
// library makes: review's critics and the local Topos agent. The library
// presents the bearer and has no part in the key's first-use wait or its
// replacement, so the first call here lists the models with the key, which
// applies both (callWithModelKey), and the session starts with a key the
// core accepted. Later calls answer that key.
func modelKeySource(modelsURL, authURL string) func(context.Context) (string, error) {
	var (
		mu       sync.Mutex
		accepted string
	)
	return func(ctx context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if accepted != "" {
			return accepted, nil
		}
		_, key, err := listModels(ctx, modelsURL, authURL)
		if err != nil {
			return "", err
		}
		accepted = key
		return accepted, nil
	}
}

// newModelsKeyCmd is `latere models key`: the current context's model key,
// and its revocation.
func newModelsKeyCmd(modelsURL, authURL *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "key",
		Short: "Show the model key this machine presents in the current context.",
		Long: `Show the key the CLI presents to the model endpoints for the current
login and context: its prefix, context, status, expiry and where it is kept.
The CLI creates it on first use and keeps it in the system keychain, or in
a 0600 file beside the login when the machine has none.

'latere models key revoke' revokes it at auth and forgets it; the next
model call creates a new one.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			l, err := modelKeyLogin(cmd.Context(), *modelsURL, *authURL)
			if err != nil {
				return err
			}
			keys := newModelKeys()
			r, ok, err := keys.Store.Get(l.Slot())
			if err != nil {
				return err
			}
			if !ok {
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "no model key for the %s context yet; the next model call creates one\n", l.Context())
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(),
				"prefix:   %s\ncontext:  %s\nstatus:   %s\nexpires:  %s\nkept in:  %s\n",
				r.Prefix, r.Context, r.Status, r.ExpiresAt.Format(time.RFC3339), keys.Store.Name())
			return err
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "revoke",
		Short: "Revoke the current context's model key at auth and forget it.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			l, err := modelKeyLogin(cmd.Context(), *modelsURL, *authURL)
			if err != nil {
				return err
			}
			r, ok, err := newModelKeys().Forget(cmd.Context(), l)
			if err != nil {
				return err
			}
			if !ok {
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "no model key for the %s context\n", l.Context())
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "revoked %s\n", r.Prefix)
			return err
		},
	})
	return cmd
}
