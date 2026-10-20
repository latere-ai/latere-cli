// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"latere.ai/x/cella/device"
	"latere.ai/x/pkg/authkit/jwt"
)

// The Latere values `latere devices` embeds Cella's device engine with: the
// keys Topos names a session under on a sandbox, the mark of a device's
// environment, how the platform's tokens carry a person and a context, and
// the refusals that are this command's own rather than the engine's. The
// platform token's audience is the SDK's latere.DefaultAudience, which the
// held credentials and the platform calls name directly. This file builds
// on every platform, since `latere logout` refuses with the codes below
// wherever it runs.

// toposLabels are the keys Topos sets on a session's sandbox, which the
// engine shows in an approval and records in the activity log.
var toposLabels = device.SessionLabels{
	Session:        "topos.latere.ai/session",
	Agent:          "topos.latere.ai/agent",
	AgentVersion:   "topos.latere.ai/agent-version",
	Trigger:        "topos.latere.ai/trigger",
	TriggerFiresOn: "topos.latere.ai/trigger-fires-on",
	AgentName:      "topos.latere.ai/agent-name",
}

// The device kind label every device environment carries beside its
// owner's tenant labels (platform spec 220).
const (
	deviceKindLabel = "platform.latere.ai/kind"
	deviceKindValue = "device"
)

// readDeviceToken reads the claims of a token a client hands `serve
// --credentials client`, as the platform's issuer writes them: aud as one
// string or a list, sub, exp in seconds, and org_id for a token in an
// organization's context. It verifies nothing; the service the token is
// presented to does.
func readDeviceToken(token string) (device.TokenClaims, error) {
	var claims struct {
		Aud   json.RawMessage `json:"aud"`
		Exp   int64           `json:"exp"`
		Sub   string          `json:"sub"`
		OrgID string          `json:"org_id"`
	}
	if err := jwt.DecodePayload(token, &claims); err != nil {
		return device.TokenClaims{}, fmt.Errorf("it is not a JWT whose claims decode: %w", err)
	}
	var audiences []string
	var one string
	if err := json.Unmarshal(claims.Aud, &one); err == nil {
		audiences = []string{one}
	} else if err = json.Unmarshal(claims.Aud, &audiences); err != nil {
		return device.TokenClaims{}, errors.New("it names no audience")
	}
	out := device.TokenClaims{Audiences: audiences, Subject: claims.Sub, Personal: claims.OrgID == "", Context: claims.OrgID}
	if claims.Exp > 0 {
		out.Expiry = time.Unix(claims.Exp, 0)
	}
	return out, nil
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
