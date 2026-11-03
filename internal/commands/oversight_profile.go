// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"cmp"
	"context"
	"errors"
	"io"
	"math"
	"strings"

	"github.com/spf13/cobra"

	"latere.ai/x/sdk-go/latere"
	"latere.ai/x/sdk-go/oversight"
)

// The profile commands read and change a trust profile (spec 024): the
// person's own, an agent's own in the context the login acts in, which an
// organization's owners and admins set, and the person's own stricter pick
// in an organization (platform spec 243, Limen spec 010).

// oversightAuditRate is the share of the steps run on their own that every
// preset shows the person to check: one in twenty, as the console's.
const oversightAuditRate = 0.05

// oversightPreset is a trust profile a person picks by name, as the
// console offers them.
type oversightPreset struct {
	name, label string
	costs       oversight.Costs
}

// oversightPresets are the console's trust profiles, Careful the one every
// person starts on. Strict's costs are 20, 4, 1 since 2026-11-03, so it
// runs a step on its own above 0.95: with Limen's default model a step's
// approval probability levels off at 0.978, below the 0.98 of the earlier
// 50, 4, 1. A profile saved with those earlier costs is no preset's: it
// reads as costs of its own, with the thresholds Limen states for it.
var oversightPresets = []oversightPreset{
	{"careful", "Careful", oversight.Costs{FalseAllow: 10, FalseBlock: 4, Ask: 1}},
	{"balanced", "Balanced", oversight.Costs{FalseAllow: 2.857, FalseBlock: 2.857, Ask: 1}},
	{"strict", "Strict", oversight.Costs{FalseAllow: 20, FalseBlock: 4, Ask: 1}},
}

// presetOf is the preset whose costs c carries, comparing their ratios to
// a question's cost, or nil for costs no preset names.
func presetOf(c oversight.Costs) *oversightPreset {
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-6 }
	if c.Ask <= 0 {
		return nil
	}
	for i, p := range oversightPresets {
		if near(p.costs.FalseAllow/p.costs.Ask, c.FalseAllow/c.Ask) && near(p.costs.FalseBlock/p.costs.Ask, c.FalseBlock/c.Ask) {
			return &oversightPresets[i]
		}
	}
	return nil
}

// profileName is a profile's name as the console says it: its preset's,
// or that its costs are no preset's.
func profileName(p *oversight.Profile) string {
	if preset := presetOf(p.Costs); preset != nil {
		return preset.label
	}
	return "costs of its own"
}

// profileTarget is the profile a profile command reads or changes.
type profileTarget struct {
	// scope is its scope: the person's own context's, an agent's own in
	// the context the login acts in, or the person's own in the
	// organization.
	scope oversight.Scope
	// agent is the agent's name as a person reads it, for --agent.
	agent string
	// forMe is the person's own stricter profile in an organization.
	forMe bool
}

// profileFlags are the flags that name a profile other than the person's
// own: --agent and --for-me.
type profileFlags struct {
	agent string
	forMe bool
}

func (f *profileFlags) add(cmd *cobra.Command, verb string) {
	cmd.Flags().StringVar(&f.agent, "agent", "", verb+" the agent's own profile in the context your login acts in, by the agent's name or id")
	cmd.Flags().BoolVar(&f.forMe, "for-me", false, verb+" your own stricter profile in the organization your login acts in")
}

// where names the context the login acts in, for a sentence.
func (c *oversightClient) where() string {
	if c.org == "" {
		return "your personal context"
	}
	return "organization " + c.org
}

// target resolves the profile the flags name. An agent is read from the
// agents API, by its name in the context the login acts in or by its id,
// since Limen keys an agent's steps by its id.
func (c *oversightClient) target(ctx context.Context, f profileFlags, agentSet bool) (profileTarget, error) {
	switch {
	case agentSet && f.forMe:
		return profileTarget{}, refusedf("--agent and --for-me name two different profiles; give one of them")
	case f.forMe:
		if c.org == "" {
			return profileTarget{}, refusedf("--for-me makes an organization's profile stricter for you, and your login acts in your personal context, where 'latere oversight profile set' chooses your own profile. Sign in to the organization with `latere login --org-id <id>`")
		}
		return profileTarget{scope: oversight.Scope{Org: c.org, Subject: c.me}, forMe: true}, nil
	case agentSet:
		name := strings.TrimSpace(f.agent)
		if name == "" {
			return profileTarget{}, refusedf("--agent needs the agent's name or id")
		}
		ag, err := newAgentsClient("", c.authURL)
		if err != nil {
			return profileTarget{}, err
		}
		a, err := ag.ag.GetAgent(ctx, name)
		if errors.Is(err, latere.ErrNotFound) {
			return profileTarget{}, refusedf("there is no agent %q in %s that you can read; 'latere agents sessions' names the agents of your sessions", name, c.where())
		}
		if err != nil {
			return profileTarget{}, ag.refused(err)
		}
		return profileTarget{
			scope: oversight.Scope{Org: cmp.Or(c.org, c.me), Agent: cmp.Or(a.Status.ID, a.Metadata.Name)},
			agent: cmp.Or(a.Metadata.DisplayName, a.Metadata.Name),
		}, nil
	}
	return profileTarget{scope: oversight.Scope{Org: c.me}}, nil
}

// refusedProfile is what a profile command says of a call that failed: a
// change of an organization's agent's profile that Limen refuses names
// who may make it.
func (c *oversightClient) refusedProfile(t profileTarget, change bool, err error) error {
	if change && t.agent != "" && c.org != "" && errors.Is(err, latere.ErrForbidden) {
		return refusedf("only an owner or an admin of organization %s can change the trust profile of its agents", c.org)
	}
	return c.refused(err)
}

func newOversightProfileCmd(apiURL, authURL *string) *cobra.Command {
	var (
		f     profileFlags
		jsonF bool
	)
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Show your trust profile, an agent's own, or your stricter one in an organization.",
		Long: `Show your trust profile: what a step that ran and that you would have
denied, a step that was stopped and that you would have allowed, and one
question cost, the thresholds that follow from them, and the share of the
steps that run on their own shown to you to check.

--agent <name> shows the profile an agent uses in the context your login
acts in: its own, which an organization's owners and admins set, or the
organization's, which it follows until it has its own. --for-me shows the
profile you picked to make your organization's ask you more, and what
applies to your own sessions: the stricter of the two.

'profile set' chooses one of the three profiles the console offers, and
'profile reset' removes an agent's own profile or your stricter one.`,
		Example: `  latere oversight profile
  latere oversight profile --agent release-notes
  latere oversight profile --for-me
  latere oversight profile set balanced`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, err := newOversightClient(ctx, *apiURL, *authURL)
			if err != nil {
				return err
			}
			t, err := c.target(ctx, f, cmd.Flags().Changed("agent"))
			if err != nil {
				return err
			}
			p, err := c.ov.GetProfile(ctx, t.scope)
			if err != nil {
				return c.refusedProfile(t, false, err)
			}
			if jsonF {
				return printRawJSON(cmd.OutOrStdout(), p.RawJSON())
			}
			out := cmd.OutOrStdout()
			switch {
			case t.forMe:
				return c.printForMe(ctx, out, p)
			case t.agent != "":
				fprintln(out, agentProfileWords(c, t, p))
			}
			printProfile(out, p, t.agent == "")
			return nil
		},
	}
	f.add(cmd, "show")
	cmd.Flags().BoolVar(&jsonF, "json", false, "print the profile as JSON")
	cmd.AddCommand(newOversightProfileSetCmd(apiURL, authURL), newOversightProfileResetCmd(apiURL, authURL))
	return cmd
}

// agentProfileWords say whose profile an agent uses: its own, or the one
// of the context it follows, from the scope Limen answered.
func agentProfileWords(c *oversightClient, t profileTarget, p *oversight.Profile) string {
	switch {
	case p.Agent != "" && c.org != "":
		return t.agent + " has its own trust profile in organization " + c.org + ", set by an owner or an admin."
	case p.Agent != "":
		return t.agent + " has its own trust profile."
	case c.org != "":
		return t.agent + " follows organization " + c.org + "'s trust profile."
	}
	return t.agent + " follows your own trust profile."
}

// printForMe says what the person picked in the organization and what
// applies to their own sessions. mine is Limen's answer to the person's
// scope: their own pick, with Subject, or the organization's when they
// picked none.
func (c *oversightClient) printForMe(ctx context.Context, out io.Writer, mine *oversight.Profile) error {
	if mine.Subject == "" {
		fprintf(out, "You have not made organization %s's profile stricter for yourself, so its profile applies to your sessions.\n", c.org)
		printProfile(out, mine, false)
		return nil
	}
	org, err := c.ov.GetProfile(ctx, oversight.Scope{Org: c.org})
	if err != nil {
		return c.refused(err)
	}
	fprintf(out, "Your own pick in organization %s: %s. Organization %s's profile: %s.\n", c.org, profileName(mine), c.org, profileName(org))
	fprintln(out, forMeWords(c.org, mine, org))
	printProfile(out, mine, false)
	return nil
}

// forMeWords say what the person's own pick does against the
// organization's profile: Limen applies the stricter of each threshold,
// the higher one to run a step and the lower one to stop it. A pick with
// costs no preset names, such as one saved as Strict before Strict's costs
// changed, is said by its thresholds, never by a preset's name.
func forMeWords(orgID string, mine, org *oversight.Profile) string {
	run := max(mine.Thresholds.AllowAbove, org.Thresholds.AllowAbove)
	stop := min(mine.Thresholds.BlockBelow, org.Thresholds.BlockBelow)
	switch {
	case run == org.Thresholds.AllowAbove && stop == org.Thresholds.BlockBelow:
		return "It changes nothing now: the organization's profile already asks you at least this much. It applies if " + orgID + " picks a looser profile."
	case run == mine.Thresholds.AllowAbove && stop == mine.Thresholds.BlockBelow && presetOf(mine.Costs) != nil:
		return "For your own sessions with its agents, Limen asks you as " + profileName(mine) + " does, the stricter of the two. With an agent that has its own profile, the stricter of that one and yours applies."
	}
	return "For your own sessions with its agents, Limen takes the stricter of each threshold: it runs a step on its own at " + percent(run) + " sure you would allow it, and stops it below " + percent(stop) + "."
}

func newOversightProfileSetCmd(apiURL, authURL *string) *cobra.Command {
	var (
		f     profileFlags
		jsonF bool
	)
	cmd := &cobra.Command{
		Use:   "set careful|balanced|strict",
		Short: "Choose your trust profile, an agent's own, or your stricter one in an organization.",
		Long: `Choose your trust profile, one of the three the console offers:

  careful    runs a step on its own at 90% sure you would allow it, and
             stops it below 25%; the one everyone starts on
  balanced   asks less: runs a step at 65% sure, and stops it below 35%
  strict     for money and production: runs a step only at 95% sure

Each shows you 1 in 20 of the steps that run on their own to check.

--agent <name> gives an agent its own profile in the context your login
acts in, for everyone's sessions with it, from its next step. It keeps
that profile when the organization's changes. In an organization, only
its owners and admins can do this.

--for-me makes your organization's profile ask you more in your own
sessions, and never less: Limen applies the stricter of the two to each
of your steps. Nobody else's sessions change, and the organization's
owners and admins do not see what you pick. A pick that is not stricter
than the organization's changes nothing until the organization picks a
looser profile.`,
		Example: `  latere oversight profile set balanced
  latere oversight profile set strict --agent release-notes
  latere oversight profile set strict --for-me
  latere oversight profile set careful --json`,
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"careful", "balanced", "strict"},
		RunE: func(cmd *cobra.Command, args []string) error {
			var preset *oversightPreset
			for i, p := range oversightPresets {
				if strings.EqualFold(args[0], p.name) {
					preset = &oversightPresets[i]
				}
			}
			if preset == nil {
				return refusedf("%q is not a trust profile; choose careful, balanced or strict", args[0])
			}
			ctx := cmd.Context()
			c, err := newOversightClient(ctx, *apiURL, *authURL)
			if err != nil {
				return err
			}
			t, err := c.target(ctx, f, cmd.Flags().Changed("agent"))
			if err != nil {
				return err
			}
			saved, err := c.ov.PutProfile(ctx, &oversight.Profile{
				Org: t.scope.Org, Agent: t.scope.Agent, Subject: t.scope.Subject,
				Costs: preset.costs, AuditRate: oversightAuditRate,
			})
			if err != nil {
				return c.refusedProfile(t, true, err)
			}
			if jsonF {
				return printRawJSON(cmd.OutOrStdout(), saved.RawJSON())
			}
			out := cmd.OutOrStdout()
			switch {
			case t.forMe:
				fprintf(out, "Organization %s's agents now ask you at least as %s does. Nobody else's sessions change.\n", c.org, preset.label)
				// What the pick does now is a note; the pick is saved
				// whether or not the organization's profile is read.
				if org, err := c.ov.GetProfile(ctx, oversight.Scope{Org: c.org}); err == nil {
					fprintln(out, forMeWords(c.org, saved, org))
				}
			case t.agent != "" && c.org != "":
				fprintf(out, "%s now has its own trust profile in organization %s: %s, for everyone's sessions with it from its next step. It keeps it when the organization's profile changes.\n", t.agent, c.org, preset.label)
			case t.agent != "":
				fprintf(out, "%s now has its own trust profile: %s, from its next step. It keeps it when your own profile changes.\n", t.agent, preset.label)
			}
			printProfile(out, saved, !t.forMe && t.agent == "")
			return nil
		},
	}
	f.add(cmd, "choose")
	cmd.Flags().BoolVar(&jsonF, "json", false, "print the saved profile as JSON")
	return cmd
}

func newOversightProfileResetCmd(apiURL, authURL *string) *cobra.Command {
	var (
		f     profileFlags
		jsonF bool
	)
	cmd := &cobra.Command{
		Use:   "reset --agent <name> | --for-me",
		Short: "Remove an agent's own trust profile, or your stricter one in an organization.",
		Long: `Remove a trust profile, so the next one applies again.

--agent <name> removes the agent's own profile in the context your login
acts in: the agent follows the organization's profile again, and its
later changes. In an organization, only its owners and admins can do
this.

--for-me removes the profile you picked to make your organization's ask
you more: the organization's profiles apply to your sessions again.

A step already decided keeps the thresholds it was decided with.
Removing a profile that is not there is no error.`,
		Example: `  latere oversight profile reset --agent release-notes
  latere oversight profile reset --for-me`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			agentSet := cmd.Flags().Changed("agent")
			if !agentSet && !f.forMe {
				return refusedf("reset removes an agent's own profile or your stricter one: give --agent <name> or --for-me. To go back to where everyone starts, run 'latere oversight profile set careful'")
			}
			ctx := cmd.Context()
			c, err := newOversightClient(ctx, *apiURL, *authURL)
			if err != nil {
				return err
			}
			t, err := c.target(ctx, f, agentSet)
			if err != nil {
				return err
			}
			if err := c.ov.DeleteProfile(ctx, t.scope); err != nil {
				return c.refusedProfile(t, true, err)
			}
			out := cmd.OutOrStdout()
			if jsonF {
				return printJSON(out, map[string]any{"org": t.scope.Org, "agent": t.scope.Agent, "subject": t.scope.Subject, "removed": true})
			}
			if t.forMe {
				fprintf(out, "Your own pick in organization %s is removed: its profiles apply to your sessions again.\n", c.org)
				return nil
			}
			whose := "your own trust profile"
			if c.org != "" {
				whose = "organization " + c.org + "'s trust profile"
			}
			// What applies now is a note; the removal is done whether or
			// not it is read.
			if now, err := c.ov.GetProfile(ctx, t.scope); err == nil {
				fprintf(out, "%s follows %s again: %s.\n", t.agent, whose, profileName(now))
				return nil
			}
			fprintf(out, "%s follows %s again.\n", t.agent, whose)
			return nil
		},
	}
	f.add(cmd, "remove")
	cmd.Flags().BoolVar(&jsonF, "json", false, "print what was removed as JSON")
	return cmd
}

// printProfile prints a profile: its name, its thresholds, its costs and
// how often it shows a step to check. own is the person's own profile in
// their own context, which they chose or did not.
func printProfile(out io.Writer, p *oversight.Profile, own bool) {
	name := profileName(p)
	switch {
	case own && presetOf(p.Costs) == nil:
		name = "Your own costs"
	case presetOf(p.Costs) == nil:
		name = "Its own costs"
	case own && p.Default:
		name += " (you have not chosen one, so this is where everyone starts)"
	case p.Default:
		name += " (the default: none is saved yet)"
	}
	var b strings.Builder
	fprintf(&b, "Trust profile: %s\n", name)
	fprintf(&b, "Runs a step on its own at %s sure you would allow it, asks between, and stops it below %s.\n",
		percent(p.Thresholds.AllowAbove), percent(p.Thresholds.BlockBelow))
	fprintf(&b, "Costs: a wrong run %g, a wrong stop %g, a question %g.\n", p.Costs.FalseAllow, p.Costs.FalseBlock, p.Costs.Ask)
	if p.AuditRate > 0 {
		fprintf(&b, "Shows you 1 in %.0f of the steps that run on their own to check.\n", 1/p.AuditRate)
	} else {
		fprint(&b, "Shows you none of the steps that run on their own to check.\n")
	}
	fprint(out, stripANSI(b.String()))
}
