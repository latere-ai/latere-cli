// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"latere.ai/x/sdk-go/memory"
)

// The memory switches, such as Remember new things, are the platform's.
// The command reads them from the settings object the platform answers,
// every boolean member in the order it sends them, rather than from a list
// of its own, so a switch the platform adds is shown before the CLI knows
// its name. It changes one through the member of the SDK's
// memory.UpdateSettingsParams that carries the switch's JSON name, so an
// SDK that adds a member is all it takes to change the switch too.

// switchSensitive is the JSON name of Include sensitive topics, whose
// turning off deletes every memory marked sensitive for good, which the
// command asks about first.
const switchSensitive = "sensitive"

// switchLabels are the names a person reads for the switches the CLI
// knows; another is shown by its own name.
var switchLabels = map[string]string{
	"remember":      "Remember new things",
	switchSensitive: "Include sensitive topics",
}

// memorySwitch is one of the person's memory switches as the platform sent
// it.
type memorySwitch struct {
	name string
	on   bool
}

// label is the name a person reads for the switch.
func (s memorySwitch) label() string {
	if l, ok := switchLabels[s.name]; ok {
		return l
	}
	words := strings.ReplaceAll(s.name, "_", " ")
	if words == "" {
		return words
	}
	return strings.ToUpper(words[:1]) + words[1:]
}

// readSwitches reads every boolean member of the JSON object obj, in the
// order it holds them.
func readSwitches(obj json.RawMessage) ([]memorySwitch, error) {
	dec := json.NewDecoder(bytes.NewReader(obj))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("parse the platform's answer: the settings are not a JSON object")
	}
	var out []memorySwitch
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("parse the platform's answer: %w", err)
		}
		name, _ := tok.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("parse the platform's answer: %w", err)
		}
		var on bool
		if json.Unmarshal(v, &on) == nil {
			out = append(out, memorySwitch{name: name, on: on})
		}
	}
	return out, nil
}

// listedSwitches reads the switches of a list's settings object.
func listedSwitches(list *memory.List) ([]memorySwitch, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(list.RawJSON(), &top); err != nil {
		return nil, fmt.Errorf("parse the platform's answer: %w", err)
	}
	settings, ok := top["settings"]
	if !ok {
		return nil, nil
	}
	return readSwitches(settings)
}

// settableSwitches maps the JSON name of each switch this SDK changes to
// the index of its member in memory.UpdateSettingsParams.
func settableSwitches() map[string]int {
	return switchFields(reflect.TypeFor[memory.UpdateSettingsParams]())
}

// switchFields maps the JSON name of each exported *bool member of the
// struct t to its index, the members a change of settings sets.
func switchFields(t reflect.Type) map[string]int {
	out := map[string]int{}
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() || f.Type != reflect.TypeFor[*bool]() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		out[name] = i
	}
	return out
}

// parseOnOff reads a switch's value as a person writes it.
func parseOnOff(v string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "true", "yes":
		return true, true
	case "off", "false", "no":
		return false, true
	}
	return false, false
}

// switchNames lists the names of the switches the platform sent and those
// this SDK changes, for a refusal that names a switch that is neither.
func switchNames(shown []memorySwitch, settable map[string]int) string {
	var names []string
	for _, s := range shown {
		names = append(names, s.name)
	}
	for name := range settable {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	slices.Sort(names[len(shown):])
	return strings.Join(names, ", ")
}

// settingsChange is a change the command was asked for, checked against
// the switches before anything is sent.
type settingsChange struct {
	params *memory.UpdateSettingsParams
	// sensitiveOff says the change turns Include sensitive topics off.
	sensitiveOff bool
}

// parseSettingsChange reads each name=on|off argument into the SDK's
// params. A name the platform shows that this SDK cannot change, a name
// that is neither, a value that is neither on nor off, and a name given
// twice are refused, so nothing is sent.
func parseSettingsChange(args []string, shown []memorySwitch) (*settingsChange, error) {
	settable := settableSwitches()
	change := &settingsChange{params: &memory.UpdateSettingsParams{}}
	v := reflect.ValueOf(change.params).Elem()
	seen := map[string]bool{}
	for _, arg := range args {
		name, value, ok := strings.Cut(arg, "=")
		name = strings.ToLower(strings.TrimSpace(name))
		if !ok || name == "" {
			return nil, fmt.Errorf("%q is not name=on or name=off; nothing was changed", arg)
		}
		if seen[name] {
			return nil, fmt.Errorf("%s is named twice; nothing was changed", name)
		}
		seen[name] = true
		on, ok := parseOnOff(value)
		if !ok {
			return nil, fmt.Errorf("%q is neither on nor off; nothing was changed", value)
		}
		i, ok := settable[name]
		if !ok {
			if slices.ContainsFunc(shown, func(s memorySwitch) bool { return s.name == name }) {
				return nil, fmt.Errorf("this version of latere cannot change %s yet, and nothing was changed. `latere upgrade` installs the newest release", name)
			}
			return nil, fmt.Errorf("%s is not a memory setting; nothing was changed. The settings are %s", name, switchNames(shown, settable))
		}
		v.Field(i).Set(reflect.ValueOf(&on))
		if name == switchSensitive && !on {
			change.sensitiveOff = true
		}
	}
	return change, nil
}

// printSwitches writes the switches as a table, saying of each the platform
// shows whether this version of the CLI can change it.
func printSwitches(out io.Writer, shown []memorySwitch) error {
	var b strings.Builder
	if len(shown) == 0 {
		b.WriteString("The platform sent no memory settings.\n")
	} else {
		settable := settableSwitches()
		w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
		fprintln(w, "SETTING\tSTATE\tWHAT IT IS")
		for _, s := range shown {
			state := "off"
			if s.on {
				state = "on"
			}
			label := s.label()
			if _, ok := settable[s.name]; !ok {
				label += " (this version of latere cannot change it)"
			}
			fprintf(w, "%s\t%s\t%s\n", s.name, state, label)
		}
		_ = w.Flush()
	}
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("write the settings: %w", err)
	}
	return nil
}

// sensitiveOn reports whether the switches have Include sensitive topics
// on.
func sensitiveOn(shown []memorySwitch) bool {
	return slices.ContainsFunc(shown, func(s memorySwitch) bool { return s.name == switchSensitive && s.on })
}

func newMemorySettingsCmd(platformURL, authURL *string) *cobra.Command {
	var yes, jsonF bool
	cmd := &cobra.Command{
		Use:   "settings [name=on|off ...]",
		Short: "Show or change Remember new things and the other memory settings.",
		Long: `Show your memory settings, or turn the ones you name on or off with
name=on or name=off:

  remember    Remember new things: conversations save new memories. Off,
              they still use what is remembered and save nothing new.
  sensitive   Include sensitive topics: memories about health, beliefs and
              the other sensitive topics are kept. Turning it off deletes
              every memory marked sensitive, for good.

The list shows every setting the platform has, including ones added after
this version of latere, which says when it cannot change one yet. Turning
Include sensitive topics off asks first unless --yes is given, and says how
many memories it deleted. --json prints the settings as the platform
answers them.`,
		Example: `  latere memory settings
  latere memory settings remember=off
  latere memory settings sensitive=off --yes`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := newMemoryClient(*platformURL, *authURL)
			if err != nil {
				return err
			}
			list, err := c.mem.ListMemories(ctx)
			if err != nil {
				return c.refused(err, memoryTarget{})
			}
			shown, err := listedSwitches(list)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(args) == 0 {
				if jsonF {
					var top map[string]json.RawMessage
					if err := json.Unmarshal(list.RawJSON(), &top); err != nil {
						return fmt.Errorf("parse the platform's answer: %w", err)
					}
					return printRawJSON(out, defaultRaw(top["settings"]))
				}
				return printSwitches(out, shown)
			}
			change, err := parseSettingsChange(args, shown)
			if err != nil {
				return err
			}
			if change.sensitiveOff && sensitiveOn(shown) && !yes {
				errOut := cmd.ErrOrStderr()
				fprint(errOut, "Turning off Include sensitive topics deletes every memory marked sensitive, for good. Turn it off? [y/N] ")
				line, err := readLine(cmd.InOrStdin())
				if err != nil {
					return fmt.Errorf("read the answer: %w", err)
				}
				if !yesTyped(line) {
					fprintln(errOut)
					return errors.New("nothing was changed. Pass --yes to change it without asking")
				}
			}
			changed, err := c.mem.UpdateSettings(ctx, change.params)
			if err != nil {
				return c.refused(err, memoryTarget{})
			}
			if jsonF {
				return printRawJSON(out, changed.RawJSON())
			}
			now, err := readSwitches(changed.RawJSON())
			if err != nil {
				return err
			}
			if err := printSwitches(out, now); err != nil {
				return err
			}
			if changed.DeletedSensitive > 0 {
				fprintf(out, "\nDeleted %s marked sensitive.\n", plural(changed.DeletedSensitive, "memory", "memories"))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "turn Include sensitive topics off without asking")
	cmd.Flags().BoolVar(&jsonF, "json", false, "print the settings as JSON")
	return cmd
}

// defaultRaw is raw, or an empty object for an answer that held none.
func defaultRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	return raw
}
