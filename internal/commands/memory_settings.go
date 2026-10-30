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

// The memory settings, such as Remember new things, are the platform's.
// The command reads them from the settings object the platform answers,
// every boolean member as a switch and every string member as a setting
// that takes a word, such as Tidy memory's review mode, in the order it
// sends them, rather than from a list of its own, so a setting the
// platform adds is shown before the CLI knows its name. It changes one
// through the member of the SDK's memory.UpdateSettingsParams that carries
// the setting's JSON name, so an SDK that adds a member is all it takes to
// change the setting too.

// switchSensitive is the JSON name of Include sensitive topics, whose
// turning off deletes every memory marked sensitive for good, which the
// command asks about first.
const switchSensitive = "sensitive"

// settingReview is the JSON name of Tidy memory's review mode.
const settingReview = "review"

// switchLabels are the names a person reads for the settings the CLI
// knows; another is shown by its own name.
var switchLabels = map[string]string{
	"remember":      "Remember new things",
	"chats":         "Search past chats",
	switchSensitive: "Include sensitive topics",
	settingReview:   "When Tidy memory changes something",
}

// settingWords are the words a setting that takes one accepts, by the
// setting's JSON name, each with what a person reads for it, as the
// console names the choices. A word another setting takes is sent as
// given, so the platform's refusal names the rule.
var settingWords = map[string][][2]string{
	settingReview: {
		{string(memory.ReviewApply), "Apply, and show me what changed"},
		{string(memory.ReviewAsk), "Ask me first"},
	},
}

// memorySwitch is one of the person's memory settings as the platform
// sent it: a switch, on or off, or a setting that takes a word.
type memorySwitch struct {
	name string
	// value is the setting as a person writes it: on or off for a switch,
	// the word itself for a setting that takes one.
	value string
	// word says the setting takes a word rather than on or off.
	word bool
}

// label is the name a person reads for the setting, with what its word
// means when the CLI knows it.
func (s memorySwitch) label() string {
	l, ok := switchLabels[s.name]
	if !ok {
		l = strings.ReplaceAll(s.name, "_", " ")
		if l != "" {
			l = strings.ToUpper(l[:1]) + l[1:]
		}
	}
	if !s.word {
		return l
	}
	for _, w := range settingWords[s.name] {
		if w[0] == s.value {
			return l + ": " + strings.ToLower(w[1][:1]) + w[1][1:]
		}
	}
	return l
}

// onOff is how a switch's state is written.
func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// readSwitches reads the settings of the JSON object obj, in the order it
// holds them: every boolean member, a switch, and every string member
// that is a setting taking a word.
func readSwitches(obj json.RawMessage) ([]memorySwitch, error) {
	settable := settableSwitches()
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
			out = append(out, memorySwitch{name: name, value: onOff(on)})
			continue
		}
		var word string
		if json.Unmarshal(v, &word) == nil && isWordSetting(name, settable) {
			out = append(out, memorySwitch{name: name, value: word, word: true})
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

// isWordSetting reports whether the string member name of the settings is
// a setting that takes a word: one the CLI names or this SDK changes. The
// settings object may hold a string that is no setting, which a switch,
// a boolean, never is.
func isWordSetting(name string, settable map[string]settingField) bool {
	_, labeled := switchLabels[name]
	return labeled || settable[name].word
}

// settingField is a member of memory.UpdateSettingsParams a change sets:
// its index, and whether it takes a word rather than on or off.
type settingField struct {
	index int
	word  bool
}

// settableSwitches maps the JSON name of each setting this SDK changes to
// its member in memory.UpdateSettingsParams.
func settableSwitches() map[string]settingField {
	return switchFields(reflect.TypeFor[memory.UpdateSettingsParams]())
}

// switchFields maps the JSON name of each exported *bool member of the
// struct t, a switch, and each exported member of a string kind, a
// setting that takes a word, to the member, the members a change of
// settings sets.
func switchFields(t reflect.Type) map[string]settingField {
	out := map[string]settingField{}
	for i := range t.NumField() {
		f := t.Field(i)
		word := f.Type.Kind() == reflect.String
		if !f.IsExported() || (f.Type != reflect.TypeFor[*bool]() && !word) {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		out[name] = settingField{index: i, word: word}
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

// switchNames lists the names of the settings the platform sent and those
// this SDK changes, for a refusal that names a setting that is neither.
func switchNames(shown []memorySwitch, settable map[string]settingField) string {
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

// parseSettingsChange reads each name=on|off or name=word argument into
// the SDK's params. A name the platform shows that this SDK cannot change,
// a name that is neither, a switch's value that is neither on nor off, a
// word the setting does not take, and a name given twice are refused, so
// nothing is sent.
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
		field, ok := settable[name]
		if !ok {
			if slices.ContainsFunc(shown, func(s memorySwitch) bool { return s.name == name }) {
				return nil, fmt.Errorf("this version of latere cannot change %s yet, and nothing was changed. `latere upgrade` installs the newest release", name)
			}
			return nil, fmt.Errorf("%s is not a memory setting; nothing was changed. The settings are %s", name, switchNames(shown, settable))
		}
		if field.word {
			word, err := parseSettingWord(name, value)
			if err != nil {
				return nil, err
			}
			v.Field(field.index).SetString(word)
			continue
		}
		on, ok := parseOnOff(value)
		if !ok {
			return nil, fmt.Errorf("%q is neither on nor off; nothing was changed", value)
		}
		v.Field(field.index).Set(reflect.ValueOf(&on))
		if name == switchSensitive && !on {
			change.sensitiveOff = true
		}
	}
	return change, nil
}

// parseSettingWord reads the word a setting that takes one was given,
// without regard to case. A word the CLI knows the setting does not take
// is refused; a setting whose words the CLI does not know takes any word
// but none, so the platform's refusal names the rule.
func parseSettingWord(name, value string) (string, error) {
	word := strings.ToLower(strings.TrimSpace(value))
	words := settingWords[name]
	if len(words) == 0 {
		if word == "" {
			return "", fmt.Errorf("%s takes a word, and none was given; nothing was changed", name)
		}
		return word, nil
	}
	known := make([]string, len(words))
	for i, w := range words {
		if w[0] == word {
			return word, nil
		}
		known[i] = w[0]
	}
	return "", fmt.Errorf("%s takes %s, not %q; nothing was changed", name, strings.Join(known, " or "), value)
}

// printSwitches writes the settings as a table, saying of each the
// platform shows whether this version of the CLI can change it.
func printSwitches(out io.Writer, shown []memorySwitch) error {
	var b strings.Builder
	if len(shown) == 0 {
		b.WriteString("The platform sent no memory settings.\n")
	} else {
		settable := settableSwitches()
		w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
		fprintln(w, "SETTING\tSTATE\tWHAT IT IS")
		for _, s := range shown {
			label := s.label()
			if _, ok := settable[s.name]; !ok {
				label += " (this version of latere cannot change it)"
			}
			fprintf(w, "%s\t%s\t%s\n", s.name, clean(s.value, false), label)
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
	return slices.ContainsFunc(shown, func(s memorySwitch) bool { return s.name == switchSensitive && s.value == "on" })
}

func newMemorySettingsCmd(platformURL, authURL *string) *cobra.Command {
	var yes, jsonF bool
	cmd := &cobra.Command{
		Use:   "settings [name=value ...]",
		Short: "Show or change Remember new things and the other memory settings.",
		Long: `Show your memory settings, or change the ones you name with name=on or
name=off, and review=apply or review=ask:

  remember    Remember new things: conversations save new memories. Off,
              they still use what is remembered and save nothing new.
  chats       Search past chats: conversations search and read your past
              conversations when you mention one or an answer depends on
              something you said before. In a project, only that project's
              conversations. Off, they read none of them.
  sensitive   Include sensitive topics: memories about health, beliefs and
              the other sensitive topics are kept. Turning it off deletes
              every memory marked sensitive, for good.
  review      When Tidy memory changes something. apply, the default: its
              changes apply at once, 'latere memory tidies' lists them, and
              you can undo them. ask: its changes wait until you apply or
              decline them. A change to a memory you wrote waits for your
              OK either way.

The list shows every on or off setting the platform has, including ones
added after this version of latere, which says when it cannot change one
yet. Turning
Include sensitive topics off asks first unless --yes is given, and says how
many memories it deleted. --json prints the settings as the platform
answers them.`,
		Example: `  latere memory settings
  latere memory settings remember=off
  latere memory settings chats=off review=ask
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
