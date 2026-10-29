// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"latere.ai/x/sdk-go/memory"
)

// Export and import move memory as files: the export is the platform's zip
// of the Markdown files, as Latere keeps them, and an import is another
// assistant's answer to the platform's import prompt, a memory per line.

// exportFallbackName is the file an export is saved to when the platform's
// answer names none.
const exportFallbackName = "latere-memory.zip"

// inputIsTerminal reports whether in is a terminal, where an import with
// no file shows the prompt and reads what is pasted. A variable so a test
// can stand in for a terminal.
var inputIsTerminal = func(in io.Reader) bool {
	f, ok := in.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func newMemoryExportCmd(platformURL, authURL *string) *cobra.Command {
	var (
		output string
		force  bool
	)
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Save every memory as one zip file.",
		Long: `Save everything Latere remembers about you and your projects as one zip file
of Markdown files, as Latere keeps them: latere-memory/you/ for your own
memory and latere-memory/projects/<project name>-<project id>/ for each
project, each with its MEMORY.md list and one file per memory.

The zip is saved in the current directory under the name the platform gives
it, such as latere-memory-2026-10-29.zip, readable only by you; -o names
another file, - for standard output. A file that is there already is kept
unless --force is given. An export the platform stops partway leaves no
file behind; run the command again.`,
		Example: `  latere memory export
  latere memory export -o ~/backups/memory.zip --force
  latere memory export -o - | unzip -l /dev/stdin`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if output != "" && output != "-" && !force {
				if err := keepExisting(output); err != nil {
					return err
				}
			}
			c, err := newMemoryClient(*platformURL, *authURL)
			if err != nil {
				return err
			}
			exp, err := c.mem.ExportMemory(cmd.Context())
			if err != nil {
				return c.refused(err, memoryTarget{})
			}
			defer func() { _ = exp.Close() }()
			if output == "-" {
				if _, err := io.Copy(cmd.OutOrStdout(), exp); err != nil {
					return exportFailed(err)
				}
				return nil
			}
			dest := output
			if dest == "" {
				dest = defaultStr(exp.FileName(), exportFallbackName)
				if !force {
					if err := keepExisting(dest); err != nil {
						return err
					}
				}
			}
			n, err := saveExport(dest, exp)
			if err != nil {
				return err
			}
			fprintf(cmd.OutOrStdout(), "Saved your memory to %s (%s).\n", dest, plural(int(n), "byte", "bytes"))
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "the file to save the zip to, - for standard output (default the name the platform gives it)")
	cmd.Flags().BoolVar(&force, "force", false, "replace a file that is there already")
	return cmd
}

// keepExisting refuses a destination that is there already, before
// anything is asked of the platform.
func keepExisting(dest string) error {
	_, err := os.Lstat(dest)
	switch {
	case err == nil:
		return fmt.Errorf("%s is there already, and nothing was exported. Name another file with -o, or pass --force to replace it", dest)
	case errors.Is(err, os.ErrNotExist):
		return nil
	default:
		return fmt.Errorf("check %s: %w", dest, err)
	}
}

// exportFailed says what an export that stopped while it was read left.
func exportFailed(err error) error {
	if errors.Is(err, memory.ErrExportIncomplete) {
		return errors.New("the export stopped before its end, so what was read is not the whole of it. Run the command again")
	}
	return fmt.Errorf("read the export: %w", err)
}

// saveExport writes the zip beside dest and renames it into place once it
// is whole, so an export that stops partway leaves no file, and answers
// the bytes written.
func saveExport(dest string, exp io.Reader) (int64, error) {
	tmp, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".*")
	if err != nil {
		return 0, fmt.Errorf("create a file beside %s: %w", dest, err)
	}
	n, copyErr := io.Copy(tmp, exp)
	closeErr := tmp.Close()
	if copyErr != nil || closeErr != nil {
		if err := os.Remove(tmp.Name()); err != nil {
			return 0, fmt.Errorf("%w; and removing %s failed: %w", exportFailedOr(copyErr, closeErr), tmp.Name(), err)
		}
		return 0, exportFailedOr(copyErr, closeErr)
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		if rmErr := os.Remove(tmp.Name()); rmErr != nil {
			return 0, fmt.Errorf("save the export as %s: %w; and removing %s failed: %w", dest, err, tmp.Name(), rmErr)
		}
		return 0, fmt.Errorf("save the export as %s: %w", dest, err)
	}
	return n, nil
}

// exportFailedOr is the error of a copy that failed, or of the close after
// it.
func exportFailedOr(copyErr, closeErr error) error {
	if copyErr != nil {
		return exportFailed(copyErr)
	}
	return fmt.Errorf("write the export: %w", closeErr)
}

// textLines is how many lines of text holds something, which is what an
// import reads a memory from.
func textLines(text string) int {
	n := 0
	for line := range strings.SplitSeq(text, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

func newMemoryImportCmd(platformURL, authURL *string) *cobra.Command {
	var (
		project string
		prompt  bool
		jsonF   bool
	)
	cmd := &cobra.Command{
		Use:   "import [file]",
		Short: "Save what another assistant remembers about you.",
		Long: `Save what another assistant remembers about you, as memories. Ask it the
words 'latere memory import --prompt' prints; it answers with one memory per
line, "Group: what it remembers", where Group is Preference, Profile, Topic,
Area, Person or Project. Save that answer to a file and import the file, or
pipe it in. Run at a terminal with no file, the command shows the words to
ask and reads what you paste, to Ctrl-D.

Each line becomes a memory of its group as it was written, with an import
as where it came from; a line with no group is a topic, and a Project line a
topic of where you import. A line you already have is left out. Lines go to
your own memory unless --project names one of your projects. An import
reads a bounded number of lines, and the command says when the text had
more, so you import the rest in a second run.`,
		Example: `  latere memory import --prompt
  latere memory import answer.txt
  pbpaste | latere memory import
  latere memory import answer.txt --project "Tide tables"`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := newMemoryClient(*platformURL, *authURL)
			if err != nil {
				return err
			}
			// The list carries the prompt and the most lines an import
			// reads, which the platform holds and the CLI does not restate.
			list, err := c.mem.ListMemories(ctx)
			if err != nil {
				return c.refused(err, memoryTarget{})
			}
			out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
			if prompt {
				fprintln(out, list.ImportPrompt)
				return nil
			}
			p, err := c.project(ctx, project, list)
			if err != nil {
				return err
			}
			var text string
			switch {
			case len(args) == 1 && args[0] != "-":
				f, err := os.Open(args[0])
				if err != nil {
					return fmt.Errorf("open the answer to import: %w", err)
				}
				text, err = readMemoryInput(f, args[0])
				if closeErr := f.Close(); err == nil && closeErr != nil {
					err = fmt.Errorf("close %s: %w", args[0], closeErr)
				}
				if err != nil {
					return err
				}
			default:
				if len(args) == 0 && inputIsTerminal(cmd.InOrStdin()) {
					fprintf(errOut, "Ask the other assistant this:\n\n%s\n\nPaste its answer here, then press Ctrl-D on a line of its own.\n", list.ImportPrompt)
				}
				if text, err = readMemoryInput(cmd.InOrStdin(), "standard input"); err != nil {
					return err
				}
			}
			if strings.TrimSpace(text) == "" {
				return errors.New("there is nothing to import: the text is empty, and nothing was sent")
			}
			scope, projectID := p.scope()
			imp, err := c.mem.ImportMemory(ctx, &memory.ImportMemoryParams{Text: text, Scope: scope, Project: projectID})
			if err != nil {
				return c.refused(err, memoryTarget{project: p})
			}
			if most := list.Limits.MaxImportLines; most > 0 {
				if n := textLines(text); n > most {
					fprintf(errOut, "The text has %d lines with something on them, and one import reads at most %d, so the rest were not read. Import the lines after the first %d in another run.\n", n, most, most)
				}
			}
			if jsonF {
				return printRawJSON(out, imp.RawJSON())
			}
			return printImported(out, imp, p)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "import into a project's memory, by the project's name or id, instead of yours")
	cmd.Flags().BoolVar(&prompt, "prompt", false, "print the words to ask the other assistant, and import nothing")
	cmd.Flags().BoolVar(&jsonF, "json", false, "print the platform's answer as JSON")
	return cmd
}

// printImported says what an import saved and left out, and lists what it
// saved.
func printImported(out io.Writer, imp *memory.Imported, p *memoryProject) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Saved %s to %s", plural(len(imp.Added), "memory", "memories"), p.label())
	if imp.Skipped > 0 {
		verb := "were"
		if imp.Skipped == 1 {
			verb = "was"
		}
		fmt.Fprintf(&b, "; %s you had already %s left out", plural(imp.Skipped, "line", "lines"), verb)
	}
	b.WriteString(".\n")
	if len(imp.Added) > 0 {
		b.WriteString("\n")
		w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
		fprintln(w, "GROUP\tID\tMEMORY")
		for _, m := range imp.Added {
			fprintf(w, "%s\t%s\t%s\n", groupHeading(m.Type), m.ID, cell(m.Description))
		}
		_ = w.Flush()
	}
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("write what was imported: %w", err)
	}
	return nil
}
