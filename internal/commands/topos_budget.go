// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"encoding/json"
	"fmt"
)

// budgetBreachMessage describes a policy stop. Missing accounting details must
// still stop print mode, and must not be rendered as a fabricated zero spend.
func budgetBreachMessage(payload json.RawMessage) (string, error) {
	var p struct {
		Leg       string   `json:"leg"`
		LimitUSD  *float64 `json:"limit_usd"`
		ActualUSD *float64 `json:"actual_usd"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", fmt.Errorf("decode BudgetBreach payload: %w", err)
	}
	message := "budget limit reached"
	if p.Leg == "usd" && p.LimitUSD != nil && p.ActualUSD != nil {
		message += fmt.Sprintf(": $%g spent (limit $%g)", *p.ActualUSD, *p.LimitUSD)
	}
	return message, nil
}
