// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// A switch the platform sends that the CLI has no label for is shown by
// its name, in the order the platform sends the switches, and marked as
// one this version cannot change.
func TestMemorySettingsShowWhatThePlatformSends(t *testing.T) {
	s := newStubMemory(t)
	s.setList(strings.Replace(memoryList, `"settings":{"remember":true,"sensitive":false}`,
		`"settings":{"search_chats":true,"remember":true,"sensitive":false,"note":"not a switch"}`, 1))
	out, _, err := runMemory(t, s, "", "settings")
	if err != nil {
		t.Fatal(err)
	}
	search, remember, sensitive := strings.Index(out, "search_chats"), strings.Index(out, "remember"), strings.Index(out, "sensitive")
	if search < 0 || remember < search || sensitive < remember {
		t.Errorf("switches out of the platform's order:\n%s", out)
	}
	contains(t, out, "Search chats (this version of latere cannot change it)", "Remember new things", "Include sensitive topics")
	if strings.Contains(out, "note") {
		t.Errorf("a member that is not a switch is shown:\n%s", out)
	}
	out, _, err = runMemory(t, s, "", "settings", "--json")
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, `"search_chats": true`, `"remember": true`)
	if got := s.requests(); slices.ContainsFunc(got, func(r string) bool { return strings.HasPrefix(r, "PATCH") }) {
		t.Errorf("showing the settings changed them: %v", got)
	}
}

// A change sends the switches named and no other, and prints the settings
// the platform answers.
func TestMemorySettingsChangeTheSwitchesNamed(t *testing.T) {
	s := newStubMemory(t)
	s.answer("PATCH /v1/account/memory/settings", http.StatusOK, `{"remember":false,"sensitive":false,"deleted_sensitive":0}`)
	out, _, err := runMemory(t, s, "", "settings", "Remember=OFF")
	if err != nil {
		t.Fatal(err)
	}
	if body := s.body("PATCH /v1/account/memory/settings"); len(body) != 1 || body["remember"] != false {
		t.Errorf("settings body = %v", body)
	}
	contains(t, out, "remember   off")
	if strings.Contains(out, "Deleted") {
		t.Errorf("a change that deleted nothing says it did:\n%s", out)
	}
}

// What cannot be sent is refused before anything is: a switch the
// platform shows and this client cannot change, a name that is no switch,
// a value that is neither on nor off, and a switch named twice.
func TestMemorySettingsRefuseWhatCannotBeSent(t *testing.T) {
	s := newStubMemory(t)
	s.setList(strings.Replace(memoryList, `"settings":{"remember":true,"sensitive":false}`,
		`"settings":{"remember":true,"sensitive":false,"search_chats":false}`, 1))
	for args, want := range map[string]string{
		"search_chats=on":              "this version of latere cannot change search_chats yet",
		"colour=on":                    "colour is not a memory setting; nothing was changed. The settings are remember, sensitive, search_chats",
		"remember=maybe":               `"maybe" is neither on nor off`,
		"remember":                     `"remember" is not name=on or name=off`,
		"remember=on remember=off":     "remember is named twice",
		"sensitive=on search_chats=on": "cannot change search_chats",
	} {
		_, _, err := runMemory(t, s, "", append([]string{"settings"}, strings.Fields(args)...)...)
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "nothing was changed") {
			t.Errorf("settings %s = %v, want %q", args, err, want)
		}
	}
	if got := s.requests(); slices.ContainsFunc(got, func(r string) bool { return strings.HasPrefix(r, "PATCH") }) {
		t.Errorf("a refused change was sent: %v", got)
	}
}

// Turning Include sensitive topics off asks first, since it deletes every
// memory marked sensitive for good, and says how many it deleted.
func TestMemorySettingsAskBeforeDeletingSensitiveMemories(t *testing.T) {
	s := newStubMemory(t)
	s.setList(strings.Replace(memoryList, `"sensitive":false}`, `"sensitive":true}`, 1))
	s.answer("PATCH /v1/account/memory/settings", http.StatusOK, `{"remember":true,"sensitive":false,"deleted_sensitive":2}`)
	_, errOut, err := runMemory(t, s, "n\n", "settings", "sensitive=off")
	if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Errorf("a no = %v", err)
	}
	contains(t, errOut, "deletes every memory marked sensitive, for good")
	if got := s.requests(); slices.ContainsFunc(got, func(r string) bool { return strings.HasPrefix(r, "PATCH") }) {
		t.Errorf("a no sent the change: %v", got)
	}
	out, _, err := runMemory(t, s, "y\n", "settings", "sensitive=off")
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "Deleted 2 memories marked sensitive.")
	if body := s.body("PATCH /v1/account/memory/settings"); len(body) != 1 || body["sensitive"] != false {
		t.Errorf("settings body = %v", body)
	}
	if _, _, err := runMemory(t, s, "", "settings", "sensitive=off", "--yes"); err != nil {
		t.Fatal(err)
	}
	// Turning it on deletes nothing and asks nothing.
	s.answer("PATCH /v1/account/memory/settings", http.StatusOK, `{"remember":true,"sensitive":true,"deleted_sensitive":0}`)
	if _, errOut, err := runMemory(t, s, "", "settings", "sensitive=on"); err != nil || errOut != "" {
		t.Errorf("turning it on = %v, asked %q", err, errOut)
	}
}

// The settings a change can set are the client's *bool members and its
// members of a string kind by their JSON names, so a member an SDK adds is
// a setting the command changes.
func TestTheSwitchesAreTheClientsMembers(t *testing.T) {
	if got := slices.Sorted(maps.Keys(settableSwitches())); !slices.Equal(got, []string{"chats", "remember", "review", "sensitive"}) {
		t.Errorf("the SDK's settings = %v", got)
	}
	type mode string
	type params struct {
		Remember    *bool   `json:"remember,omitempty"`
		SearchChats *bool   `json:"search_chats,omitempty"`
		Note        *string `json:"note,omitempty"`
		Mode        mode    `json:"mode,omitempty"`
		Skipped     *bool   `json:"-"`
		Untagged    *bool
		hidden      *bool
	}
	_ = params{}.hidden
	got := switchFields(reflect.TypeFor[params]())
	want := map[string]settingField{"remember": {index: 0}, "search_chats": {index: 1}, "mode": {index: 3, word: true}}
	if !maps.Equal(got, want) {
		t.Errorf("switchFields = %v, want %v", got, want)
	}
}

// Search past chats is a switch, and Tidy memory's review mode a setting
// that takes a word: both are shown with what they mean, and a change
// sends the word or the switch as given.
func TestMemorySettingsChatsAndReview(t *testing.T) {
	s := newStubMemory(t)
	s.setList(strings.Replace(memoryList, `"settings":{"remember":true,"sensitive":false}`,
		`"settings":{"remember":true,"chats":true,"sensitive":false,"review":"apply"}`, 1))
	out, _, err := runMemory(t, s, "", "settings")
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "chats      on     Search past chats\n",
		"review     apply  When Tidy memory changes something: apply, and show me what changed\n")
	if strings.Contains(out, "cannot change") {
		t.Errorf("a setting this version changes is marked as one it cannot:\n%s", out)
	}

	s.answer("PATCH /v1/account/memory/settings", http.StatusOK, `{"remember":true,"chats":false,"sensitive":false,"review":"ask","deleted_sensitive":0}`)
	out, _, err = runMemory(t, s, "", "settings", "chats=off", "Review=ASK")
	if err != nil {
		t.Fatal(err)
	}
	if body := s.body("PATCH /v1/account/memory/settings"); len(body) != 2 || body["chats"] != false || body["review"] != "ask" {
		t.Errorf("settings body = %v", body)
	}
	contains(t, out, "chats      off", "review     ask    When Tidy memory changes something: ask me first")

	for args, want := range map[string]string{
		"review=never": `review takes apply or ask, not "never"; nothing was changed`,
		"review=":      `review takes apply or ask, not ""; nothing was changed`,
		"chats=ask":    `"ask" is neither on nor off; nothing was changed`,
	} {
		before := len(s.requests())
		_, _, err := runMemory(t, s, "", "settings", args)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("settings %s = %v, want %q", args, err, want)
		}
		if got := s.requests()[before:]; slices.ContainsFunc(got, func(r string) bool { return strings.HasPrefix(r, "PATCH") }) {
			t.Errorf("settings %s sent the change: %v", args, got)
		}
	}
}

// A setting that takes a word the CLI has no list of words for takes any
// word but none, and a word setting the platform adds that neither the CLI
// nor the SDK names is no setting the command shows.
func TestMemorySettingWords(t *testing.T) {
	if got, err := parseSettingWord("tone", " Brief "); err != nil || got != "brief" {
		t.Errorf("parseSettingWord(tone, brief) = %q, %v", got, err)
	}
	if _, err := parseSettingWord("tone", " "); err == nil || !strings.Contains(err.Error(), "tone takes a word, and none was given") {
		t.Errorf("parseSettingWord(tone, empty) = %v", err)
	}
	got, err := readSwitches([]byte(`{"review":"ask","tone":"brief","remember":false}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []memorySwitch{{name: "review", value: "ask", word: true}, {name: "remember", value: "off"}}
	if !slices.Equal(got, want) {
		t.Errorf("readSwitches = %v, want %v", got, want)
	}
	if l := (memorySwitch{name: "review", value: "later", word: true}).label(); l != "When Tidy memory changes something" {
		t.Errorf("the label of a word the CLI does not know = %q", l)
	}
}
