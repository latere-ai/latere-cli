// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package devices

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// The audiences of the tokens a client hands an engine under
// `serve --credentials client`: Cella's, for the environment's keys, and the
// platform origin's, for the device's record.
const (
	AudienceCella    = "cella"
	AudiencePlatform = "api.latere.ai"
)

// What a client reads for each audience in the status.
const (
	CredentialValid  = "valid"
	CredentialNeeded = "needed"
)

// credentialsMargin is how long before its expiry a held token counts as
// needed, so a client has time to hand a new one before a call fails.
const credentialsMargin = time.Minute

// CredentialsWait is how long a call that needs a token waits for a client
// to hand one before it fails.
const CredentialsWait = 30 * time.Second

// ErrCredentialsNeeded is a call that waited for a token no client handed.
var ErrCredentialsNeeded = errors.New("no client handed a valid token for this audience in time")

// ErrNoClientCredentials is a token handed to an engine that serves with the
// saved login, which takes none from a client.
var ErrNoClientCredentials = errors.New("this engine serves with the saved login and takes no token from a client")

// CredentialsError is a token a client handed that the engine refuses: not
// addressed to its audience, or expired, or with no expiry at all.
type CredentialsError struct{ Reason string }

func (e *CredentialsError) Error() string { return e.Reason }

// HeldToken is one token a client handed: the bearer and when it expires.
type HeldToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// CredentialsState is, for each audience, whether the engine holds a token it
// can use or needs a new one.
type CredentialsState struct {
	Cella    string `json:"cella"`
	Platform string `json:"platform"`
}

// Credentials holds the tokens a client handed the engine, in memory only:
// never written to a file or the activity log.
type Credentials struct {
	now  func() time.Time
	wait time.Duration

	mu   sync.Mutex
	held map[string]HeldToken
	// changed is closed and replaced at each Put, waking every waiting call.
	changed chan struct{}
	timers  map[string]*time.Timer
	// onNeed tells the engine's clients that a token turned needed.
	onNeed func()
}

// NewCredentials builds an empty store. A nil now is time.Now, and a zero
// wait is CredentialsWait.
func NewCredentials(now func() time.Time, wait time.Duration) *Credentials {
	if now == nil {
		now = time.Now
	}
	if wait <= 0 {
		wait = CredentialsWait
	}
	return &Credentials{now: now, wait: wait, held: map[string]HeldToken{}, changed: make(chan struct{}), timers: map[string]*time.Timer{}}
}

// notifyNeed sets what is called each time a held token turns needed.
func (c *Credentials) notifyNeed(f func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onNeed = f
}

// Put keeps the tokens given, by audience, after checking every one: a token
// for another audience, or one that expires within a minute, is refused and
// nothing is kept.
func (c *Credentials) Put(tokens map[string]HeldToken) error {
	now := c.now()
	checked := map[string]HeldToken{}
	for audience, t := range tokens {
		expiry, err := checkToken(audience, t, now)
		if err != nil {
			return err
		}
		checked[audience] = HeldToken{Token: t.Token, ExpiresAt: expiry}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for audience, t := range checked {
		c.held[audience] = t
		if timer := c.timers[audience]; timer != nil {
			timer.Stop()
		}
		c.timers[audience] = time.AfterFunc(t.ExpiresAt.Add(-credentialsMargin).Sub(now), func() {
			c.mu.Lock()
			f := c.onNeed
			c.mu.Unlock()
			if f != nil {
				f()
			}
		})
	}
	close(c.changed)
	c.changed = make(chan struct{})
	return nil
}

// Token answers a usable token for audience, waiting for a client to hand
// one when none is held, up to the store's wait.
func (c *Credentials) Token(ctx context.Context, audience string) (string, error) {
	deadline := time.NewTimer(c.wait)
	defer deadline.Stop()
	for {
		c.mu.Lock()
		t, ok := c.held[audience]
		changed := c.changed
		c.mu.Unlock()
		if ok && c.now().Add(credentialsMargin).Before(t.ExpiresAt) {
			return t.Token, nil
		}
		select {
		case <-changed:
		case <-deadline.C:
			return "", fmt.Errorf("%w: %s", ErrCredentialsNeeded, audience)
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// State is, for each audience, whether a usable token is held.
func (c *Credentials) State() CredentialsState {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	state := func(audience string) string {
		if t, ok := c.held[audience]; ok && now.Add(credentialsMargin).Before(t.ExpiresAt) {
			return CredentialValid
		}
		return CredentialNeeded
	}
	return CredentialsState{Cella: state(AudienceCella), Platform: state(AudiencePlatform)}
}

// checkToken reads a handed token's claims without verifying its signature,
// which the service it is presented to does: it must be addressed to the
// audience and expire more than a minute from now. Its expiry is the earlier
// of its exp claim and the one the client gave.
func checkToken(audience string, t HeldToken, now time.Time) (time.Time, error) {
	if strings.TrimSpace(t.Token) == "" {
		return time.Time{}, &CredentialsError{Reason: "the " + audience + " token is empty"}
	}
	parts := strings.Split(t.Token, ".")
	if len(parts) < 2 {
		return time.Time{}, &CredentialsError{Reason: "the " + audience + " token is not a JWT"}
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, &CredentialsError{Reason: "the " + audience + " token's claims do not decode: " + err.Error()}
	}
	var claims struct {
		Aud json.RawMessage `json:"aud"`
		Exp int64           `json:"exp"`
	}
	if err = json.Unmarshal(raw, &claims); err != nil {
		return time.Time{}, &CredentialsError{Reason: "the " + audience + " token's claims do not decode: " + err.Error()}
	}
	var auds []string
	var one string
	if json.Unmarshal(claims.Aud, &one) == nil {
		auds = []string{one}
	} else if err = json.Unmarshal(claims.Aud, &auds); err != nil {
		return time.Time{}, &CredentialsError{Reason: "the " + audience + " token names no audience"}
	}
	if !slices.Contains(auds, audience) {
		return time.Time{}, &CredentialsError{Reason: fmt.Sprintf("the %s token is addressed to %v, not to %s", audience, auds, audience)}
	}
	expiry := t.ExpiresAt
	if claims.Exp > 0 {
		if exp := time.Unix(claims.Exp, 0); expiry.IsZero() || exp.Before(expiry) {
			expiry = exp
		}
	}
	if expiry.IsZero() {
		return time.Time{}, &CredentialsError{Reason: "the " + audience + " token names no expiry"}
	}
	if !now.Add(credentialsMargin).Before(expiry) {
		return time.Time{}, &CredentialsError{Reason: fmt.Sprintf("the %s token expires at %s, within a minute", audience, expiry.UTC().Format(time.RFC3339))}
	}
	return expiry.UTC(), nil
}
