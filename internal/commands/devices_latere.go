// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"errors"
	"fmt"

	"latere.ai/x/cella/device"
	"latere.ai/x/sdk-go/agents"
	"latere.ai/x/sdk-go/latere"
)

// The Latere values `latere devices` embeds Cella's device engine with, in
// the engine's types: the keys Topos names a session under on a sandbox,
// and how the platform's tokens carry a person and a context. The values
// themselves are the SDK's, as are the device kind label and the platform
// token's audience, which the commands name directly. Beside them are the
// refusals that are this command's own rather than the engine's. This file
// builds on every platform, since `latere logout` refuses with the codes
// below wherever it runs.

// toposLabels are the keys Topos sets on a session's sandbox, which the
// engine shows in an approval and records in the activity log.
var toposLabels = device.SessionLabels{
	Session:        agents.LabelSession,
	Agent:          agents.LabelAgent,
	AgentVersion:   agents.LabelAgentVersion,
	Trigger:        agents.LabelTrigger,
	TriggerFiresOn: agents.LabelTriggerFiresOn,
	AgentName:      agents.AnnotationAgentName,
}

// readDeviceToken reads the claims of a token a client hands `serve
// --credentials client` with the SDK's reader of the platform's tokens:
// aud as one string or a list, sub, exp when it names one, and org_id for
// a token in an organization's context. A token that names no audience is
// refused here. It verifies nothing; the service the token is presented to
// does.
func readDeviceToken(token string) (device.TokenClaims, error) {
	claims, err := latere.ReadUnverifiedClaims(token)
	if err != nil {
		return device.TokenClaims{}, fmt.Errorf("it is not a JWT whose claims decode: %w", err)
	}
	if len(claims.Audiences) == 0 {
		return device.TokenClaims{}, errors.New("it names no audience")
	}
	return device.TokenClaims{
		Audiences: claims.Audiences, Subject: claims.Subject, Expiry: claims.Expiry,
		Personal: claims.OrgID == "", Context: claims.OrgID,
	}, nil
}

// The refusals of `latere devices` and `latere logout` that the engine does
// not know: a build that cannot verify its own upgrades, and a sign-out on a
// connected computer that was cancelled or could not remove it.
const (
	codeUnverifiedBuild = "device_unverified_build"
	codeLogoutCancelled = "device_logout_cancelled"
	codeLogoutBlocked   = "device_logout_blocked"
)

// cliRefusals are the sentences and next steps of the codes above.
var cliRefusals = map[string]struct{ message, hint string }{
	codeUnverifiedBuild: {
		"This build of latere cannot verify its own upgrades, so it cannot connect this computer.",
		"Install latere from https://latere.ai and connect again.",
	},
	codeLogoutCancelled: {
		"Nothing was changed: you are still signed in, and this computer is still connected.",
		"",
	},
	codeLogoutBlocked: {
		"This computer could not be removed from your devices, so you are still signed in.",
		"Try again, or remove it with `latere devices remove` and then sign out.",
	},
}

// cliRefusal builds the refusal for one of the codes above, as device.Fail
// builds the engine's own, so device.CodeOf and the CLI's printing read it
// the same way.
func cliRefusal(code, detail string, err error) *device.Error {
	r := cliRefusals[code]
	if detail == "" && err != nil {
		detail = err.Error()
	}
	return &device.Error{Code: code, Message: r.message, Hint: r.hint, Detail: detail, Err: err}
}
