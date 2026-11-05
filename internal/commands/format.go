// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// outputJSON is how every --json output is written: indented, map keys in
// order so a run prints the same bytes as the last, and the platform's own
// raw answers and the text in them taken as they come (a raw value may repeat
// a key, and a string may carry bytes that are not UTF-8).
var outputJSON = json.JoinOptions(rawJSONOpts, jsontext.WithIndent("  "))

// rawJSONOpts writes values that hold the platform's raw answers or text
// from outside: map keys in order, a raw value that repeats a key, and a
// string that is not UTF-8, all taken as they are.
var rawJSONOpts = json.JoinOptions(
	json.Deterministic(true),
	jsontext.AllowDuplicateNames(true),
	jsontext.AllowInvalidUTF8(true),
)

// foreignJSON reads JSON this CLI does not produce: a model's tool-call
// input, a proxy's refusal, a provider's answer, a file a person edited. A
// repeated key (the last wins), text that is not UTF-8 and any letter case
// in a name are read as they come.
var foreignJSON = json.JoinOptions(
	jsontext.AllowDuplicateNames(true),
	jsontext.AllowInvalidUTF8(true),
	json.MatchCaseInsensitiveNames(true),
)

// printJSON writes v as indented JSON followed by a newline, the form every
// --json output takes.
func printJSON(out io.Writer, v any) error {
	return json.MarshalEncode(jsontext.NewEncoder(out, outputJSON), v)
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
