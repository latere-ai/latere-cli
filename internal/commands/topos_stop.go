// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

// stopFailureMessage explains terminal stops that do not mean the requested
// work completed, even when the server emits no RunError.
func stopFailureMessage(reason string) string {
	switch reason {
	case "budget_exceeded":
		return "budget limit reached"
	case "max_tokens":
		return "model output reached its token limit; response may be incomplete"
	case "tool_use":
		return "agent stopped while requesting tools; work may be incomplete"
	default:
		return ""
	}
}
