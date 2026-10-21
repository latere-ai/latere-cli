// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

// The variables that point a capability's commands somewhere other than the
// hosted platform, for a development deployment or a test. Each wins over the
// default and loses to the command's own flag.
const (
	// envEnvironmentsURL is the Environments API's base URL.
	envEnvironmentsURL = "LATERE_ENVIRONMENTS_URL"
	// envEnvironmentsToken is a bearer presented to Environments as given.
	envEnvironmentsToken = "LATERE_ENVIRONMENTS_TOKEN"
	// envAgentProviderFile is where the local agent's provider choice is kept.
	envAgentProviderFile = "LATERE_AGENT_PROVIDER_FILE"
	// envAppsURL is the Apps API's base URL.
	envAppsURL = "LATERE_APPS_URL"
	// envAppsToken is a bearer presented to Apps as given.
	envAppsToken = "LATERE_APPS_TOKEN"
)
