// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

//go:build darwin || linux

package devices

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// What a desktop client does through the control socket that the terminal
// does with `latere devices connect`, `latere devices policy` and `latere
// devices remove` (spec 018): connect this computer with the consent the
// client showed, hand the engine its tokens, change the host policy, and
// remove the device. Each goes through the same function the command does.

// Enroller is what the engine asks of the command for the steps that reach
// the platform as the person: the proposal's defaults, the connect after the
// person consented, a new name, and the removal.
type Enroller interface {
	// Propose fills a connect's terms from the query, then what the previous
	// host policy file said, then the defaults, as `connect` fills its
	// flags.
	Propose(previous Policy, q ConnectQuery) (Policy, error)
	// Connect does what `connect` does after its typed yes: the host policy
	// file, the environment at Cella and its name at the platform. It
	// answers the policy with its device.
	Connect(ctx context.Context, p Policy) (Policy, error)
	// Rename sets the device's name at the platform.
	Rename(ctx context.Context, env, name string) error
	// Remove removes the device at the platform, which deletes its
	// environment at Cella and revokes its keys.
	Remove(ctx context.Context, env string) error
}

// ConnectQuery is what a client asks a proposal for, as the query of
// GET /v1/connect: the values `connect` takes as flags. An empty value is
// the previous file's, then the default.
type ConnectQuery struct {
	Name, Network, SessionStart string
	// Folders are PATH or PATH:ro, each absolute or under ~.
	Folders, Hosts []string
}

// Terms are what a person consents to when this computer is connected.
type Terms struct {
	Name         string   `json:"name"`
	SessionStart string   `json:"session_start"`
	Folders      []Folder `json:"folders"`
	Network      Network  `json:"network"`
}

// TermsOf are a policy's terms.
func TermsOf(p Policy) Terms {
	t := Terms{Name: p.Name, SessionStart: p.SessionStart, Folders: p.Folders, Network: p.Network}
	if t.SessionStart == "" {
		t.SessionStart = SessionStartAllow
	}
	if t.Folders == nil {
		t.Folders = []Folder{}
	}
	if t.Network.Hosts == nil {
		t.Network.Hosts = []string{}
	}
	return t
}

// Proposal is what GET /v1/connect answers: the terms, the facts the person
// is shown with them, the digest a connect presents to say the person saw
// both, and what is wrong with the terms, if anything.
type Proposal struct {
	Policy   Terms    `json:"policy"`
	Facts    []Fact   `json:"facts"`
	Consent  string   `json:"consent"`
	Problems []string `json:"problems"`
}

// The kinds of fact the person is shown before this computer is connected.
const (
	FactFolders = "folders"
	FactSecrets = "secrets"
	FactNetwork = "network"
	FactWho     = "who"
	FactLog     = "log"
)

// Fact is one thing the person is told before this computer is connected:
// a sentence, what it names (the folders, the hosts), and a sentence after
// them. The terminal's connect screen and GET /v1/connect render the same
// facts, so the two never say different things.
type Fact struct {
	Kind  string   `json:"kind"`
	Text  string   `json:"text"`
	Items []string `json:"items,omitempty"`
	Note  string   `json:"note,omitempty"`
	// inline prints the items on one line, joined, as the screen prints the
	// hosts.
	inline bool
}

// screen is the fact as the connect screen prints it.
func (f Fact) screen() string {
	var b strings.Builder
	b.WriteString(f.Text + "\n")
	switch {
	case f.inline && len(f.Items) > 0:
		b.WriteString("  " + strings.Join(f.Items, ", ") + "\n")
	default:
		for _, item := range f.Items {
			b.WriteString("  " + item + "\n")
		}
	}
	if f.Note != "" {
		b.WriteString(f.Note + "\n")
	}
	return b.String()
}

// connectFacts are the facts for a policy, in the connect screen's line
// breaks.
func connectFacts(p Policy) []Fact {
	folders := Fact{Kind: FactFolders, Text: "Sessions you start in Latere will run commands and read and edit files\non this computer, in these folders only:"}
	for _, f := range p.Folders {
		access := "read-only"
		if f.Write {
			access = "read-write"
		}
		folders.Items = append(folders.Items, fmt.Sprintf("%s (%s)", f.Path, access))
	}
	network := Fact{Kind: FactNetwork, Text: "Network: none. Commands on this computer reach no host."}
	if p.Network.Mode != NetworkNone && len(p.Network.Hosts) > 0 {
		network = Fact{
			Kind: FactNetwork, Text: "Network: commands may reach these hosts only:", Items: p.Network.Hosts, inline: true,
			Note: "A host that accepts writes, such as a git host, can carry data out.",
		}
	}
	who := "Only sessions you start, and triggers you own, can use this computer.\n"
	if p.SessionStart == SessionStartAsk {
		who += sessionStartAskWords + "\n" + switchToAllow
	} else {
		who += sessionStartAllowWords + "\n" +
			"Anyone who can start sessions as you can reach these folders, still\n" +
			"confined to them: never your credentials, keychains or browser profiles,\n" +
			"and no network beyond what is listed above.\n" + switchToAsk
	}
	return []Fact{
		folders,
		{Kind: FactSecrets, Text: "Everything in these folders is readable to the agent, a project's .env\nfile and other secrets kept there included."},
		network,
		{Kind: FactWho, Text: who},
		{Kind: FactLog, Text: "What the agent reads on this computer goes to the session's log and to\nthe model. In an organization's conversation, that organization can\nread it too."},
	}
}

// ConnectFacts are the facts for a policy as a client shows them: each
// sentence on one line, for the client to wrap.
func ConnectFacts(p Policy) []Fact {
	facts := connectFacts(p)
	for i := range facts {
		facts[i].Text = strings.ReplaceAll(facts[i].Text, "\n", " ")
		facts[i].Note = strings.ReplaceAll(facts[i].Note, "\n", " ")
	}
	return facts
}

// Consent is the digest of terms and the facts shown with them, which a
// connect through the socket presents to say the person saw both.
func Consent(t Terms, facts []Fact) string {
	b, err := json.Marshal(struct {
		Terms Terms  `json:"terms"`
		Facts []Fact `json:"facts"`
	}{t, facts})
	if err != nil {
		// Terms and facts are strings, booleans and slices of them, which
		// always encode.
		panic(err)
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ErrNotRemembered is a remembered trigger the host policy names none of.
var ErrNotRemembered = errors.New("no remembered trigger has this id")

// errClientRemoved ends a device's run that a client removed through the
// control socket.
var errClientRemoved = errors.New("the device was removed through the control socket")

// checkPolicy lists what is wrong with a policy: its own shape, then the
// local driver's folder, read path and network rules.
func (e *Engine) checkPolicy(p Policy) []string {
	problems := p.Problems(e.cfg.Paths.Home)
	if len(problems) == 0 {
		if err := CheckDriver(p, e.cfg.Paths, e.cfg.GOOS, e.cfg.NewDriver); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if problems == nil {
		problems = []string{}
	}
	return problems
}

// enroller is the engine's Enroller, refusing an engine built without one.
func (e *Engine) enroller() (Enroller, error) {
	if e.cfg.Enroll == nil {
		return nil, Fail(CodeServeFailed, "this engine was built without a way to reach the platform", nil)
	}
	return e.cfg.Enroll, nil
}

// unconnectedPolicy reads the host policy file of a computer not connected
// yet, refusing one that is.
func (e *Engine) unconnectedPolicy() (Policy, error) {
	path := e.cfg.Paths.PolicyFile()
	previous, ok, err := LoadPolicy(path)
	if err != nil {
		return Policy{}, Fail(CodePolicyInvalid, "", err)
	}
	if ok && previous.Device != "" {
		return Policy{}, Fail(CodeAlreadyConnected, path+" names the device "+previous.Device, nil)
	}
	return previous, nil
}

// ProposeConnect is GET /v1/connect: the terms for the query, filled as
// `connect` fills its flags and checked as it checks them, with the facts
// the person is shown and the consent a connect presents.
func (e *Engine) ProposeConnect(q ConnectQuery) (Proposal, error) {
	enroll, err := e.enroller()
	if err != nil {
		return Proposal{}, err
	}
	for _, f := range q.Folders {
		if !strings.HasPrefix(f, "/") && f != "~" && !strings.HasPrefix(f, "~/") {
			return Proposal{}, Fail(CodePolicyInvalid, fmt.Sprintf("the folder %q is neither absolute nor under ~", f), nil)
		}
	}
	previous, err := e.unconnectedPolicy()
	if err != nil {
		return Proposal{}, err
	}
	p, err := enroll.Propose(previous, q)
	if err != nil {
		return Proposal{}, Fail(CodePolicyInvalid, "", err)
	}
	terms, facts := TermsOf(p), ConnectFacts(p)
	return Proposal{Policy: terms, Facts: facts, Consent: Consent(terms, facts), Problems: e.checkPolicy(p)}, nil
}

// Connect is POST /v1/connect: with a consent that matches the terms and
// the facts the engine would show for them now, it connects this computer
// as `connect` does after its typed yes, and starts serving it.
func (e *Engine) Connect(ctx context.Context, t Terms, consent string) (Status, error) {
	enroll, err := e.enroller()
	if err != nil {
		return Status{}, err
	}
	if !e.connectMu.TryLock() {
		return Status{}, Fail(CodeAlreadyConnected, "another connect is in progress", nil)
	}
	defer e.connectMu.Unlock()
	previous, err := e.unconnectedPolicy()
	if err != nil {
		return Status{}, err
	}
	p := previous
	p.Device, p.Name, p.SessionStart, p.Folders, p.Network = "", t.Name, t.SessionStart, t.Folders, t.Network
	if p.Network.Mode == NetworkNone {
		p.Network.Hosts = nil
	}
	if problems := e.checkPolicy(p); len(problems) > 0 {
		return Status{}, Fail(CodePolicyInvalid, strings.Join(problems, "; "), nil)
	}
	if Consent(TermsOf(p), ConnectFacts(p)) != consent {
		return Status{}, Fail(CodeNotConfirmed, "the consent does not match the terms and the facts this engine shows for them now; ask GET /v1/connect again and show its answer", nil)
	}
	if _, err = enroll.Connect(ctx, p); err != nil {
		return Status{}, err
	}
	select {
	case e.connected <- struct{}{}:
	default:
	}
	return e.Status(), nil
}

// PutCredentials is PUT /v1/credentials: the tokens a client hands an engine
// under `serve --credentials client`, by "cella" and "platform".
func (e *Engine) PutCredentials(tokens map[string]HeldToken) (Status, error) {
	if e.cfg.Credentials == nil {
		return Status{}, ErrNoClientCredentials
	}
	byAudience := map[string]HeldToken{}
	for key, t := range tokens {
		switch key {
		case "cella":
			byAudience[AudienceCella] = t
		case "platform":
			byAudience[AudiencePlatform] = t
		default:
			return Status{}, &CredentialsError{Reason: fmt.Sprintf("%q names no audience; hand cella and platform", key)}
		}
	}
	if err := e.cfg.Credentials.Put(byAudience); err != nil {
		return Status{}, err
	}
	status := e.Status()
	e.hub.publish(Event{Kind: EventStatus, Status: &status})
	return status, nil
}

// Policy is GET /v1/policy: the host policy as the engine reads it.
func (e *Engine) Policy() (Policy, error) {
	e.policyMu.Lock()
	defer e.policyMu.Unlock()
	return e.connectedPolicy()
}

// connectedPolicy reads the host policy file of a connected computer. The
// caller holds policyMu.
func (e *Engine) connectedPolicy() (Policy, error) {
	path := e.cfg.Paths.PolicyFile()
	p, ok, err := LoadPolicy(path)
	if err != nil {
		return Policy{}, Fail(CodePolicyInvalid, "", err)
	}
	if !ok || p.Device == "" {
		return Policy{}, Fail(CodeNotConnected, path+" names no device", nil)
	}
	return p, nil
}

// PolicyChange is what PUT /v1/policy changes; a field left out keeps its
// value.
type PolicyChange struct {
	Name    *string   `json:"name"`
	Folders *[]Folder `json:"folders"`
	Network *Network  `json:"network"`
}

// PutPolicy is PUT /v1/policy: the change is checked as `connect` checks a
// policy, a new name is set at the platform, and the file is written and
// read again by the engine serving it.
func (e *Engine) PutPolicy(ctx context.Context, c PolicyChange) (Status, error) {
	e.policyMu.Lock()
	p, err := e.connectedPolicy()
	if err != nil {
		e.policyMu.Unlock()
		return Status{}, err
	}
	renamed := false
	if c.Name != nil {
		name := CleanName(*c.Name)
		renamed = name != p.Name
		p.Name = name
	}
	if c.Folders != nil {
		p.Folders = nil
		for _, f := range *c.Folders {
			path, err := Expand(e.cfg.Paths.Home, f.Path)
			if err != nil {
				e.policyMu.Unlock()
				return Status{}, Fail(CodePolicyInvalid, "folders: "+err.Error(), nil)
			}
			p.Folders = append(p.Folders, Folder{Path: Shorten(e.cfg.Paths.Home, path), Write: f.Write})
		}
	}
	if c.Network != nil {
		p.Network = *c.Network
		if p.Network.Mode == NetworkNone {
			p.Network.Hosts = nil
		}
	}
	if problems := e.checkPolicy(p); len(problems) > 0 {
		e.policyMu.Unlock()
		return Status{}, Fail(CodePolicyInvalid, strings.Join(problems, "; "), nil)
	}
	if renamed {
		enroll, err := e.enroller()
		if err == nil {
			err = enroll.Rename(ctx, p.Device, p.Name)
		}
		if err != nil {
			e.policyMu.Unlock()
			return Status{}, Fail(CodeRenameFailed, "", err)
		}
	}
	err = SavePolicy(e.cfg.Paths.PolicyFile(), p)
	e.policyMu.Unlock()
	if err != nil {
		return Status{}, Fail(CodePolicyInvalid, "write the host policy file", err)
	}
	e.mu.Lock()
	serving := e.running && e.policy.Device == p.Device
	if serving {
		// The name the status shows changes at once; the folders and the
		// network with the reload.
		e.policy.Name = p.Name
	}
	e.mu.Unlock()
	if serving {
		if err := e.Reload(); err != nil {
			return Status{}, err
		}
	}
	status := e.Status()
	e.hub.publish(Event{Kind: EventStatus, Status: &status})
	return status, nil
}

// ForgetRemembered is DELETE /v1/policy/remembered/{trigger}: the trigger
// asks again at its next run.
func (e *Engine) ForgetRemembered(trigger string) error {
	e.policyMu.Lock()
	defer e.policyMu.Unlock()
	p, err := e.connectedPolicy()
	if err != nil {
		return err
	}
	if !p.Forget(trigger) {
		return ErrNotRemembered
	}
	if err = SavePolicy(e.cfg.Paths.PolicyFile(), p); err != nil {
		return Fail(CodePolicyInvalid, "write the host policy file", err)
	}
	return nil
}

// Remove is POST /v1/remove: what `latere devices remove` does for this
// computer. The device is removed at the platform, unless local is set for
// a device that belongs to another account or is gone there already; then
// every sandbox stops, the key and the host policy file are deleted, the
// activity log stays, and `serve` waits to be connected again.
func (e *Engine) Remove(ctx context.Context, local bool) (Status, error) {
	e.mu.Lock()
	env, cancel, done := e.policy.Device, e.devCancel, e.devDone
	e.mu.Unlock()
	if env == "" {
		e.policyMu.Lock()
		p, err := e.connectedPolicy()
		e.policyMu.Unlock()
		if err != nil {
			return Status{}, err
		}
		env = p.Device
	}
	if !local {
		enroll, err := e.enroller()
		if err == nil {
			err = enroll.Remove(ctx, env)
		}
		if err != nil {
			return Status{}, Fail(CodeRemoveFailed, "", err)
		}
	}
	if cancel != nil {
		// The device's run stops every sandbox and forgets the device here
		// before it returns.
		cancel(errClientRemoved)
		<-done
	} else if err := e.forgetHere(env); err != nil {
		return Status{}, Fail(CodeKeyUnavailable, "the device was removed, and on this computer: "+err.Error(), err)
	}
	return e.Status(), nil
}

// forgetHere deletes the key and the host policy file of a device removed
// through the control socket.
func (e *Engine) forgetHere(env string) error {
	e.policyMu.Lock()
	defer e.policyMu.Unlock()
	return Forget(e.cfg.Keys, e.cfg.Paths.PolicyFile(), env, true)
}
