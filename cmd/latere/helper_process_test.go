// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"testing"

	"github.com/latere-ai/latere-cli/internal/commands"
)

// TestConfiguredOutputHelperProcess runs the command tree with its output set
// to the file LATERE_TEST_DOWNLOAD_OUTPUT names, opened read-only unless
// LATERE_TEST_DOWNLOAD_WRITABLE is 1, so a test can check that a command
// writes to its configured output and reports a failed write. It does nothing
// when run as an ordinary test.
func TestConfiguredOutputHelperProcess(t *testing.T) {
	output := os.Getenv("LATERE_TEST_DOWNLOAD_OUTPUT")
	if output == "" {
		return
	}
	flags := os.O_RDONLY
	if os.Getenv("LATERE_TEST_DOWNLOAD_WRITABLE") == "1" {
		flags = os.O_WRONLY | os.O_APPEND
	}
	file, err := os.OpenFile(output, flags, 0600)
	if err != nil {
		t.Fatal(err)
	}
	root := commands.NewRoot("test")
	root.SetOut(file)
	root.SetArgs(os.Args[3:])
	err = root.Execute()
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Exit(commands.HandleExitError(os.Stderr, err))
	}
	os.Exit(0)
}

// TestConfiguredInputHelperProcess runs the command tree with its input set to
// the file LATERE_TEST_CONFIGURED_INPUT names, so a test can check that a
// command reads its configured input rather than the process's stdin. It does
// nothing when run as an ordinary test.
func TestConfiguredInputHelperProcess(t *testing.T) {
	source := os.Getenv("LATERE_TEST_CONFIGURED_INPUT")
	if source == "" {
		return
	}
	file, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	root := commands.NewRoot("test")
	root.SetIn(file)
	root.SetArgs(os.Args[3:])
	err = root.Execute()
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Exit(commands.HandleExitError(os.Stderr, err))
	}
	os.Exit(0)
}
