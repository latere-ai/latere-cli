// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// printJSON writes v as indented JSON, the form every --json output takes.
func printJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// printWrappedField writes one labeled field to stdout.
func printWrappedField(label, value string) {
	fprintf(os.Stdout, "%s", formatWrappedField(label, value))
}

// formatWrappedField renders one "label: value" record line, the value folded
// to one line and wrapped under its label. An empty value renders nothing, so
// a record lists only the fields that hold something.
func formatWrappedField(label, value string) string {
	value = oneLine(value)
	if value == "" {
		return ""
	}
	const (
		labelWidth = 12
		maxWidth   = 88
	)
	prefix := fmt.Sprintf("%-*s", labelWidth, label+":")
	lines := wrapText(value, maxWidth-labelWidth)
	if len(lines) == 0 {
		return prefix + "\n"
	}
	indent := strings.Repeat(" ", labelWidth)
	return prefix + strings.Join(lines, "\n"+indent) + "\n"
}

// wrapText breaks s into lines of at most width characters at word
// boundaries.
func wrapText(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	if width <= 0 {
		width = 76
	}
	var lines []string
	line := words[0]
	for _, word := range words[1:] {
		if len(line)+1+len(word) > width {
			lines = append(lines, line)
			line = word
			continue
		}
		line += " " + word
	}
	lines = append(lines, line)
	return lines
}

// oneLine collapses every run of whitespace into one space.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// humanAge is how long ago t was, in its largest whole unit.
func humanAge(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// defaultStr is s, or fallback when s is empty.
func defaultStr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
