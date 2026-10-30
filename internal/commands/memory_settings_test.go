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

// The switches a change can set are the client's *bool members by their
// JSON names, so a member an SDK adds is a switch the command changes.
func TestTheSwitchesAreTheClientsMembers(t *testing.T) {
	if got := slices.Sorted(maps.Keys(settableSwitches())); !slices.Equal(got, []string{"chats", "remember", "sensitive"}) {
		t.Errorf("the SDK's switches = %v", got)
	}
	type params struct {
		Remember    *bool   `json:"remember,omitempty"`
		SearchChats *bool   `json:"search_chats,omitempty"`
		Note        *string `json:"note,omitempty"`
		Skipped     *bool   `json:"-"`
		Untagged    *bool
		hidden      *bool
	}
	_ = params{}.hidden
	got := switchFields(reflect.TypeFor[params]())
	if want := map[string]int{"remember": 0, "search_chats": 1}; !maps.Equal(got, want) {
		t.Errorf("switchFields = %v, want %v", got, want)
	}
}
