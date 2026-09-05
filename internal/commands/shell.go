// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"errors"
	"strings"
)

// quoteShellValue encodes a literal value for a POSIX shell assignment.
// Ordinary URLs and JWTs retain their readable unquoted form.
func quoteShellValue(value string) (string, error) {
	if strings.ContainsRune(value, 0) {
		return "", errors.New("cannot export a shell value containing NUL")
	}
	const safe = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-"
	if value != "" && strings.Trim(value, safe) == "" {
		return value, nil
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'", nil
}
