// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestTheModelKeyOpensNothingButTheModelService holds ModelGrant to the
// model service's actions alone. The key is created at auth and carries no
// budget reference, and the platform gives such a key a spending limit
// only while every action it names is the model service's (platform
// specs/185, decision 5). One that could also start a session could be
// given no limit, because nothing would tie the session's spend to the
// key. A key for more than models is created through the platform, which
// adds the reference.
func TestTheModelKeyOpensNothingButTheModelService(t *testing.T) {
	var grants []struct {
		Actions []string `json:"actions"`
	}
	if err := json.Unmarshal([]byte(ModelGrant), &grants); err != nil {
		t.Fatalf("ModelGrant does not read as a list of grants: %v", err)
	}
	if len(grants) == 0 {
		t.Fatal("ModelGrant names no grant, and a key with none acts with its holder's whole reach")
	}
	for i, g := range grants {
		if len(g.Actions) == 0 {
			t.Errorf("grant %d names no action", i)
		}
		for _, action := range g.Actions {
			if !strings.HasPrefix(action, "lux:") {
				t.Errorf("grant %d names %s, which is not the model service's: a key made here with it can be given no spending limit", i, action)
			}
		}
	}
}
