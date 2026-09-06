// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
)

// fakeClipboard isolates every clipboard reader the dependency can select on
// Unix. No test reads or changes the user's system clipboard.
func fakeClipboard(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("fake clipboard command requires a Unix shell")
	}
	dir := t.TempDir()
	for _, name := range []string{"pbpaste", "xclip", "xsel", "wl-paste", "termux-clipboard-get", "powershell.exe"} {
		body := "#!/bin/sh\nprintf '%s' 'clipboard text'\n"
		if name == "powershell.exe" {
			body += "printf '\\r\\n'\n" // The Unix fallback strips the Windows newline.
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	unsupported := clipboard.Unsupported
	clipboard.Unsupported = false
	t.Cleanup(func() { clipboard.Unsupported = unsupported })
}

func TestTUIDelayedClipboardPreservesApprovalDraft(t *testing.T) {
	fakeClipboard(t)
	m, sender := newTestModel(false)
	m.input.SetValue("draft ")
	updated, paste := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	m = updated.(tuiModel)
	if paste == nil {
		t.Fatal("paste command was not started")
	}
	m.state.apply(ev("ApprovalRequest", `{"decision_id":"d1","tool_id":"bash"}`))
	updated, _ = m.Update(paste()) // Clipboard read completes after approval arrives.
	m = updated.(tuiModel)
	if m.input.Value() != "draft " || len(sender.sent) != 0 || m.state.pending == nil {
		t.Fatalf("delayed paste changed hidden draft: input=%q sent=%v pending=%v", m.input.Value(), sender.sent, m.state.pending)
	}
	updated, _ = m.Update(keyRunes("n"))
	m = updated.(tuiModel)
	updated, paste = m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	m = updated.(tuiModel)
	if paste == nil {
		t.Fatal("paste did not resume after approval")
	}
	updated, _ = m.Update(paste())
	m = updated.(tuiModel)
	if m.input.Value() != "draft clipboard text" {
		t.Fatalf("visible draft paste failed: %q", m.input.Value())
	}
}
