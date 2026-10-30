// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"latere.ai/x/sdk-go/latere"
	"latere.ai/x/sdk-go/memory"
)

// Tidy memory, an Always on item, goes through one scope of the person's
// memory each night: one tidy, a Run of the SDK's memory package, with
// each memory it changed. The commands list the tidies, show what one
// changed as a diff of each memory's file, and apply, decline or undo its
// changes through the runs routes under /v1/account/runs. The words a
// person reads are the console's, so every surface names a tidy, a change
// and their states the same way.

// tidySourcePrefix starts the source of a memory a tidy wrote, followed
// by the tidy's id. The platform writes it; the SDK names no constant.
const tidySourcePrefix = "dream:"

// tidyOf is the id of the tidy a memory's source names, "" for a source
// that names none.
func tidyOf(source string) string {
	id, ok := strings.CutPrefix(source, tidySourcePrefix)
	if !ok {
		return ""
	}
	return clean(id, false)
}

// tidyMinute is how the time a tidy ran is shown, and tidyDay the day
// what waits is declined, both in the computer's local time: a tidy runs
// at night in the person's time zone, which a UTC time can put on the day
// before.
const (
	tidyMinute = "2006-01-02 15:04"
	tidyDay    = "2006-01-02"
)

// The words a person reads for a tidy, a change and where each stands, as
// the console words them.
var (
	tidyStateWords = map[memory.RunState]string{
		memory.RunRunning:       "Tidying",
		memory.RunApplied:       "Applied",
		memory.RunWaiting:       "Waits for you",
		memory.RunPartlyApplied: "Partly applied",
		memory.RunDeclined:      "Declined",
		memory.RunUndone:        "Undone",
	}
	changeKindWords = map[memory.ChangeKind]string{
		memory.ChangeCreated: "Added",
		memory.ChangeChanged: "Updated",
		memory.ChangeRemoved: "Removed",
	}
	changeStateWords = map[memory.ChangeState]string{
		memory.ChangeApplied:      "Applied",
		memory.ChangeWaiting:      "Waits for you",
		memory.ChangeDeclined:     "Declined",
		memory.ChangeChangedSince: "You changed it since",
		memory.ChangeUndone:       "Undone",
	}
	reviewWords = map[memory.Review]string{
		memory.ReviewApply: "Apply, and show me what changed",
		memory.ReviewAsk:   "Ask me first",
	}
)

// wordOf is the word a person reads for k, or k as sent for one the CLI
// has no word for, such as a state the platform adds.
func wordOf[K ~string](words map[K]string, k K) string {
	if w, ok := words[k]; ok {
		return w
	}
	return clean(string(k), false)
}

// The sentences a tidy's changes are explained with.
const (
	tidyHeldNote         = "You wrote this memory, so this change waits for your OK."
	tidyChangedSinceNote = "You changed this memory after Tidy memory read it, so it was left as it is."
	tidyMovedNote        = "You changed this memory since, so it was left as it is."
	tidyNothingSentence  = "Nothing needed changing: Tidy memory went through this memory and left it as it was."
)

// tidiedAt is when a tidy ran: when it ended, or when it started for one
// that has not ended.
func tidiedAt(r *memory.Run) time.Time {
	if !r.EndedAt.IsZero() {
		return r.EndedAt
	}
	return r.CreatedAt
}

// localTime is t in the computer's local time in layout, "-" for none.
func localTime(t time.Time, layout string) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format(layout)
}

// wait is the verb of what waits for the person, for n of it.
func wait(n int) string {
	if n == 1 {
		return "waits"
	}
	return "wait"
}

// tidySummary is what a tidy did, in a few words: "1 added, 2 updated, 1
// removed", what waits for the person, or why nothing changed.
func tidySummary(r *memory.Run) string {
	if r.State == memory.RunRunning {
		return "Tidying now"
	}
	c := r.Counts
	if c.Waiting > 0 && c.Waiting == c.Added+c.Updated+c.Removed {
		return fmt.Sprintf("%s %s for you", plural(c.Waiting, "change", "changes"), wait(c.Waiting))
	}
	var parts []string
	for _, p := range []struct {
		n    int
		word string
	}{{c.Added, "added"}, {c.Updated, "updated"}, {c.Removed, "removed"}} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.word))
		}
	}
	done := strings.Join(parts, ", ")
	switch {
	case c.Waiting > 0 && done == "":
		return fmt.Sprintf("%s %s for you", plural(c.Waiting, "change", "changes"), wait(c.Waiting))
	case c.Waiting > 0:
		return fmt.Sprintf("%s; %d %s for you", done, c.Waiting, wait(c.Waiting))
	case done != "":
		return done
	case r.State == memory.RunDeclined:
		return "Declined, nothing changed"
	case r.State == memory.RunUndone:
		return "Undone, nothing changed"
	default:
		return "Nothing needed changing"
	}
}

// tidyScope names whose memory a tidy went through: You, or the project
// by its name when names holds it, else by its id.
func tidyScope(r *memory.Run, names map[string]string) string {
	if r.Scope != memory.ScopeProject {
		return "You"
	}
	if name := clean(names[r.Project], false); name != "" {
		return name
	}
	return r.Project
}

// projectNames reads the names of the projects the runs went through,
// from the memory list, which names the person's projects; nil without a
// call when every run is of You.
func (c *memoryClient) projectNames(cmd *cobra.Command, runs []*memory.Run) (map[string]string, error) {
	if !slices.ContainsFunc(runs, func(r *memory.Run) bool { return r.Scope == memory.ScopeProject }) {
		return nil, nil
	}
	list, err := c.mem.ListMemories(cmd.Context())
	if err != nil {
		return nil, c.refused(err, memoryTarget{})
	}
	names := make(map[string]string, len(list.Projects))
	for _, p := range list.Projects {
		names[p.ID] = p.Name
	}
	return names, nil
}

// ---- the memory before and after ----

// memoryHeader splits a memory's file into its header's members, in the
// order the file holds them, and the rest, as the platform reads a memory
// file: a member is a line "key: value", a value in double quotes is a
// quoted string, and one in single quotes doubles a quote. ok is false
// for a file without a header.
func memoryHeader(file string) (members [][2]string, rest string, ok bool) {
	lines := strings.Split(strings.TrimPrefix(file, "\ufeff"), "\n")
	if strings.TrimSpace(lines[0]) != "---" {
		return nil, file, false
	}
	for i := 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "---" {
			return members, strings.Join(lines[i+1:], "\n"), true
		}
		key, value, found := strings.Cut(line, ":")
		if !found || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "#") {
			continue
		}
		members = append(members, [2]string{strings.TrimSpace(key), unquoteMember(strings.TrimSpace(value))})
	}
	return nil, file, false
}

// unquoteMember reads a header member's value as the platform does.
func unquoteMember(v string) string {
	switch {
	case len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"':
		if s, err := strconv.Unquote(v); err == nil {
			return s
		}
		return v[1 : len(v)-1]
	case len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'':
		return strings.ReplaceAll(v[1:len(v)-1], "''", "'")
	}
	return v
}

// readableMemory is a memory's file as a person reads it, a line each, for
// the comparison of before and after, as the console shows it: its name,
// the memory itself, its group, where it came from and whether it is
// about a sensitive topic, each with its label, then a member the CLI has
// no word for by its key, so no change hides, then the detail after a
// blank line. A file without a header reads as it is; an empty one, a
// memory not there, has no lines.
func readableMemory(file string) []string {
	file = strings.TrimRight(clean(file, true), "\n")
	if file == "" {
		return nil
	}
	members, rest, ok := memoryHeader(file)
	if !ok {
		return strings.Split(file, "\n")
	}
	value := map[string]string{}
	for _, m := range members {
		value[m[0]] = clean(m[1], false)
	}
	var lines []string
	for _, f := range []struct {
		key, label string
		show       func(string) string
	}{
		{"name", "Name", func(v string) string { return v }},
		{"description", "Memory", func(v string) string { return v }},
		{"type", "Group", func(v string) string { return groupHeading(memory.Type(v)) }},
		{"source", "From", memorySource},
	} {
		if v, ok := value[f.key]; ok {
			lines = append(lines, f.label+": "+f.show(v))
		}
	}
	if value["sensitive"] == "true" {
		lines = append(lines, "About a sensitive topic")
	}
	for _, m := range members {
		switch m[0] {
		case "name", "description", "type", "source", "sensitive":
		default:
			lines = append(lines, m[0]+": "+clean(m[1], false))
		}
	}
	if detail := strings.Trim(rest, "\n"); detail != "" {
		lines = append(lines, "")
		lines = append(lines, strings.Split(detail, "\n")...)
	}
	return lines
}

// diffLine is one line of a comparison: ' ' for a line of both, '-' for
// one only before, '+' for one only after.
type diffLine struct {
	op   byte
	text string
}

// maxDiffCells bounds the table a comparison fills, lines before times
// lines after. A memory's file is held to a few kilobytes, so only a file
// far past that bound reaches it; such a pair is shown as all of before
// removed and all of after added.
const maxDiffCells = 1 << 20

// diffLines compares before and after line by line, keeping the longest
// run of lines both hold, a line removed before the line added in its
// place.
func diffLines(before, after []string) []diffLine {
	n, m := len(before), len(after)
	out := make([]diffLine, 0, n+m)
	if (n+1)*(m+1) > maxDiffCells {
		for _, l := range before {
			out = append(out, diffLine{'-', l})
		}
		for _, l := range after {
			out = append(out, diffLine{'+', l})
		}
		return out
	}
	// common[i][j] is the length of the longest run of lines both
	// before[i:] and after[j:] hold.
	common := make([][]int, n+1)
	for i := range common {
		common[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if before[i] == after[j] {
				common[i][j] = common[i+1][j+1] + 1
			} else {
				common[i][j] = max(common[i+1][j], common[i][j+1])
			}
		}
	}
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case before[i] == after[j]:
			out = append(out, diffLine{' ', before[i]})
			i++
			j++
		case common[i+1][j] >= common[i][j+1]:
			out = append(out, diffLine{'-', before[i]})
			i++
		default:
			out = append(out, diffLine{'+', after[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, diffLine{'-', before[i]})
	}
	for ; j < m; j++ {
		out = append(out, diffLine{'+', after[j]})
	}
	return out
}

// writeDiff writes the comparison of a memory's file before and after,
// indented under its change.
func writeDiff(b *strings.Builder, before, after string) {
	for _, l := range diffLines(readableMemory(before), readableMemory(after)) {
		prefix := "    "
		if l.op != ' ' {
			prefix = "  " + string(l.op) + " "
		}
		b.WriteString(strings.TrimRight(prefix+l.text, " ") + "\n")
	}
}

// ---- what is printed ----

// writeTidyList writes runs as one table, newest first as the platform
// lists them.
func writeTidyList(b *strings.Builder, runs []*memory.Run, names map[string]string) {
	w := tabwriter.NewWriter(b, 0, 4, 2, ' ', 0)
	fprintln(w, "ID\tIN\tTIDIED\tSTATE\tCHANGED")
	for _, r := range runs {
		fprintf(w, "%s\t%s\t%s\t%s\t%s\n", clean(r.ID, false), tidyScope(r, names), localTime(tidiedAt(r), tidyMinute),
			wordOf(tidyStateWords, r.State), tidySummary(r))
	}
	_ = w.Flush()
}

// printTidies writes the tidies, with what to run next.
func printTidies(out io.Writer, runs []*memory.Run, names map[string]string, p *memoryProject) error {
	var b strings.Builder
	switch {
	case len(runs) == 0 && p == nil:
		b.WriteString("Tidy memory has not tidied your memory yet. Each night it goes through what Latere remembers about you, and each project with something new; a night with nothing new starts nothing.\n")
	case len(runs) == 0:
		fmt.Fprintf(&b, "Tidy memory has not tidied %s yet. Each night it goes through a project's memory when the project has something new.\n", p.label())
	default:
		writeTidyList(&b, runs, names)
		waiting := 0
		for _, r := range runs {
			if r.Counts.Waiting > 0 {
				waiting++
			}
		}
		switch {
		case waiting == 1:
			b.WriteString("1 tidy has changes that wait for you: `latere memory tidies apply <id>` applies them, and `latere memory tidies decline <id>` declines them.\n")
		case waiting > 1:
			fmt.Fprintf(&b, "%d tidies have changes that wait for you: `latere memory tidies apply <id>` applies them, and `latere memory tidies decline <id>` declines them.\n", waiting)
		}
		b.WriteString("`latere memory tidies show <id>` shows what a tidy changed, each memory before and after.\n")
	}
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("write the tidies: %w", err)
	}
	return nil
}

// tidyIn names whose memory a tidy went through, for its record: You, or
// the project by its name and id.
func tidyIn(r *memory.Run, names map[string]string) string {
	if r.Scope != memory.ScopeProject {
		return "You"
	}
	if name := clean(names[r.Project], false); name != "" {
		return fmt.Sprintf("%s (%s)", name, r.Project)
	}
	return "the project " + r.Project
}

// changeNote is why a change stands where it does, "" for a change that
// needs no word.
func changeNote(c *memory.RunChange, moved bool) string {
	switch {
	case moved:
		return tidyMovedNote
	case c.State == memory.ChangeChangedSince:
		return tidyChangedSinceNote
	case c.Held == memory.HeldYours && c.State == memory.ChangeWaiting:
		return tidyHeldNote
	}
	return ""
}

// countChanges is how many of the run's changes are in state.
func countChanges(r *memory.Run, state memory.ChangeState) int {
	n := 0
	for _, c := range r.Changes {
		if c.State == state {
			n++
		}
	}
	return n
}

// printTidy writes one tidy as a record, then each change it made with
// where it stands and the memory before and after.
func printTidy(out io.Writer, r *memory.Run, names map[string]string) error {
	var b strings.Builder
	id := clean(r.ID, false)
	b.WriteString(formatWrappedField("tidy", id))
	b.WriteString(formatWrappedField("in", tidyIn(r, names)))
	b.WriteString(formatWrappedField("state", wordOf(tidyStateWords, r.State)))
	b.WriteString(formatWrappedField("tidied", localTime(tidiedAt(r), tidyMinute)))
	if r.Review != "" {
		b.WriteString(formatWrappedField("review", wordOf(reviewWords, r.Review)))
	}
	b.WriteString(formatWrappedField("changed", tidySummary(r)))
	switch {
	case r.State == memory.RunRunning:
		fmt.Fprintf(&b, "\nTidy memory is still going through this memory. What it changes shows here when it is done: run `latere memory tidies show %s` again then.\n", id)
	case len(r.Changes) == 0:
		b.WriteString("\n" + tidyNothingSentence + "\n")
	default:
		if countChanges(r, memory.ChangeWaiting) > 0 {
			b.WriteString("\nThese changes wait for you")
			if !r.ExpiresAt.IsZero() {
				b.WriteString(" until " + localTime(r.ExpiresAt, tidyDay))
			}
			fmt.Fprintf(&b, ". If you do nothing, nothing changes.\n`latere memory tidies apply %s` applies them and `latere memory tidies decline %s` declines them; name memories after the id to decide only those.\n", id, id)
		} else if countChanges(r, memory.ChangeApplied) > 0 {
			fmt.Fprintf(&b, "\n`latere memory tidies undo %s` takes back what it applied; name memories after the id to undo only those.\n", id)
		}
		for i := range r.Changes {
			c := &r.Changes[i]
			fmt.Fprintf(&b, "\n%s %s: %s\n", wordOf(changeKindWords, c.Kind), clean(c.ID, false), wordOf(changeStateWords, c.State))
			if note := changeNote(c, false); note != "" {
				b.WriteString(note + "\n")
			}
			writeDiff(&b, c.Before, c.After)
		}
	}
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("write the tidy: %w", err)
	}
	return nil
}

// writeChangeStates writes each change of a run with where it stands, the
// changes in moved marked as left as they were.
func writeChangeStates(b *strings.Builder, r *memory.Run, moved []string) {
	w := tabwriter.NewWriter(b, 0, 4, 2, ' ', 0)
	fprintln(w, "CHANGE\tMEMORY\tSTATE")
	for i := range r.Changes {
		c := &r.Changes[i]
		state := wordOf(changeStateWords, c.State)
		if slices.Contains(moved, c.ID) {
			state = "Left as it is: you changed it since"
		}
		fprintf(w, "%s\t%s\t%s\n", wordOf(changeKindWords, c.Kind), clean(c.ID, false), state)
	}
	_ = w.Flush()
}

// ---- deciding ----

// tidyDecision is one of the decisions on a tidy's changes.
type tidyDecision string

const (
	decideApply   tidyDecision = "apply"
	decideDecline tidyDecision = "decline"
	decideUndo    tidyDecision = "undo"
)

// decides is the state a change the decision acts on is in before it, and
// the state it leaves the change in.
func (d tidyDecision) decides() (from, to memory.ChangeState) {
	switch d {
	case decideApply:
		return memory.ChangeWaiting, memory.ChangeApplied
	case decideDecline:
		return memory.ChangeWaiting, memory.ChangeDeclined
	default:
		return memory.ChangeApplied, memory.ChangeUndone
	}
}

// decided counts the changes the decision moved from its state to its
// outcome, comparing the run before with the run after, and the changes it
// left as they were because the person changed the memory since: an
// apply's that are now ChangeChangedSince, and an undo's in moved.
func (d tidyDecision) decided(before, after *memory.Run, moved []string) (done, left int) {
	from, to := d.decides()
	was := make(map[string]memory.ChangeState, len(before.Changes))
	for _, c := range before.Changes {
		was[c.ID] = c.State
	}
	for _, c := range after.Changes {
		if was[c.ID] != from {
			continue
		}
		switch {
		case c.State == to:
			done++
		case d == decideApply && c.State == memory.ChangeChangedSince:
			left++
		}
	}
	if d == decideUndo {
		left = len(moved)
	}
	return done, left
}

// sentence says what the decision did: how many changes, and the
// memories left as they were.
func (d tidyDecision) sentence(done, left int) string {
	verb := map[tidyDecision]string{decideApply: "applied", decideDecline: "declined", decideUndo: "undone"}[d]
	var s string
	switch done {
	case 0:
		s = "Nothing was " + verb
	case 1:
		s = "The change is " + verb
	default:
		s = fmt.Sprintf("The %d changes are %s", done, verb)
	}
	if left == 0 {
		s += "."
	} else {
		leftAs := "was left as it is"
		if left > 1 {
			leftAs = "were left as they are"
		}
		sep := ". "
		if done == 0 {
			sep = ": "
		}
		s += sep + plural(left, "memory", "memories") + " you changed since " + leftAs + "."
	}
	if d == decideDecline && done > 0 {
		s += " Nothing changed in your memory."
	}
	return s
}

// undoQuestion is what undo asks before it takes back more than one
// change.
const undoQuestion = "Undo the %d changes the tidy %s applied? The memories go back to how they were before it; a memory you changed since stays as it is. [y/N] "

func newMemoryTidyDecisionCmd(platformURL, authURL *string, d tidyDecision) *cobra.Command {
	var yes, jsonF bool
	cmd := &cobra.Command{
		Use:  string(d) + " <tidy> [memory ...]",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, named := strings.TrimSpace(args[0]), args[1:]
			if id == "" {
				return errors.New("the tidy's id is empty; nothing was sent. `latere memory tidies` lists your tidies")
			}
			ctx := cmd.Context()
			c, err := newMemoryClient(*platformURL, *authURL)
			if err != nil {
				return err
			}
			about := memoryTarget{tidy: id, changes: named}
			before, err := c.mem.GetRun(ctx, id)
			if err != nil {
				return c.refused(err, about)
			}
			if err := knownChanges(before, named); err != nil {
				return err
			}
			if d == decideUndo && !yes {
				if n := undoable(before, named); n > 1 {
					errOut := cmd.ErrOrStderr()
					fprintf(errOut, undoQuestion, n, clean(id, false))
					line, err := readLine(cmd.InOrStdin())
					if err != nil {
						return fmt.Errorf("read the answer: %w", err)
					}
					if !yesTyped(line) {
						fprintln(errOut)
						return errors.New("nothing was undone. Pass --yes to undo without asking")
					}
				}
			}
			var params *memory.RunDecisionParams
			if len(named) > 0 {
				params = &memory.RunDecisionParams{Changes: named}
			}
			var (
				after *memory.Run
				moved []string
				raw   []byte
			)
			switch d {
			case decideApply, decideDecline:
				decide := c.mem.ApplyRun
				if d == decideDecline {
					decide = c.mem.DeclineRun
				}
				if after, err = decide(ctx, id, params); err != nil {
					return decisionFailed(c.refused(err, about), err, id)
				}
				raw = after.RawJSON()
			default:
				undone, err := c.mem.UndoRun(ctx, id, params)
				if err != nil {
					return c.refused(err, about)
				}
				after, moved, raw = &undone.Run, undone.MovedOn, undone.RawJSON()
			}
			out := cmd.OutOrStdout()
			if jsonF {
				return printRawJSON(out, raw)
			}
			var b strings.Builder
			b.WriteString(d.sentence(d.decided(before, after, moved)) + "\n\n")
			writeChangeStates(&b, after, moved)
			if _, err := io.WriteString(out, b.String()); err != nil {
				return fmt.Errorf("write the tidy: %w", err)
			}
			return nil
		},
	}
	switch d {
	case decideApply:
		cmd.Short = "Apply what a tidy changed that waits for you."
		cmd.Long = `Apply what a tidy changed that waits for you: the changes to the memories
named after the tidy, by their ids as 'latere memory tidies show' prints
them, or every change that waits. A memory you changed after Tidy memory
read it is left as it is. The command says what it applied and lists each
change as it stands now; --json prints the platform's answer.`
		cmd.Example = `  latere memory tidies apply ses_01JB8K4Q2M6N9P3R5S7T1V3X5Z
  latere memory tidies apply ses_01JB8K4Q2M6N9P3R5S7T1V3X5Z mia`
	case decideDecline:
		cmd.Short = "Decline what a tidy changed that waits for you."
		cmd.Long = `Decline what a tidy changed that waits for you: the changes to the memories
named after the tidy, by their ids as 'latere memory tidies show' prints
them, or every change that waits. Nothing changes in your memory, and a
declined change cannot be applied later. The command lists each change as
it stands now; --json prints the platform's answer.`
		cmd.Example = `  latere memory tidies decline ses_01JB8K4Q2M6N9P3R5S7T1V3X5Z
  latere memory tidies decline ses_01JB8K4Q2M6N9P3R5S7T1V3X5Z mia`
	default:
		cmd.Short = "Take back what a tidy changed."
		cmd.Long = `Take back what a tidy applied: a memory it added is removed, one it updated
gets its text back, and one it removed is restored. Name memories after the
tidy, by their ids as 'latere memory tidies show' prints them, to undo only
those; without, every change it applied is undone. A memory you changed
since is left as it is, and the command says so.

Undoing more than one change asks first unless --yes is given. --json prints
the platform's answer.`
		cmd.Example = `  latere memory tidies undo ses_01JB8K4Q2M6N9P3R5S7T1V3X5Z
  latere memory tidies undo ses_01JB8K4Q2M6N9P3R5S7T1V3X5Z mia
  latere memory tidies undo ses_01JB8K4Q2M6N9P3R5S7T1V3X5Z --yes`
		cmd.Flags().BoolVar(&yes, "yes", false, "undo more than one change without asking")
	}
	cmd.Flags().BoolVar(&jsonF, "json", false, "print the platform's answer as JSON")
	return cmd
}

// undoable is how many changes an undo of the memories named, every one
// when none is, takes back: those applied.
func undoable(r *memory.Run, named []string) int {
	n := 0
	for _, c := range r.Changes {
		if c.State == memory.ChangeApplied && (len(named) == 0 || slices.Contains(named, c.ID)) {
			n++
		}
	}
	return n
}

// knownChanges refuses, before anything is sent, a memory the tidy did not
// change.
func knownChanges(r *memory.Run, named []string) error {
	var unknown []string
	for _, id := range named {
		if !slices.ContainsFunc(r.Changes, func(c memory.RunChange) bool { return c.ID == id }) {
			unknown = append(unknown, clean(id, false))
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	id := clean(r.ID, false)
	return fmt.Errorf("the tidy %s changed no memory %s; nothing was sent. `latere memory tidies show %s` lists the memories it changed",
		id, strings.Join(unknown, ", "), id)
}

// decisionFailed is what an apply or a decline says when its answer
// failed: a refusal as it is, and any other failure with what to run,
// since the decision is sent once and may have been saved before the
// answer failed. A token that could not be minted sent nothing.
func decisionFailed(said, err error, id string) error {
	if _, ok := errors.AsType[*latere.TokenError](err); ok {
		return said
	}
	if pe, ok := errors.AsType[*platformError](said); ok && pe.Status < 500 {
		return said
	}
	return fmt.Errorf("%w\nThe decision may have been saved before the answer failed. `latere memory tidies show %s` shows where each change stands", said, clean(id, false))
}

// ---- the commands ----

// tidiesList is the list of tidies, which `latere memory tidies` and
// `latere memory tidies list` both run.
type tidiesList struct {
	project string
	jsonF   bool
}

// bind adds the list's flags to cmd.
func (o *tidiesList) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&o.project, "project", "", "list a project's tidies, by the project's name or id")
	cmd.Flags().BoolVar(&o.jsonF, "json", false, "print the platform's answer as JSON")
}

// run lists the tidies.
func (o *tidiesList) run(platformURL, authURL *string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		c, err := newMemoryClient(*platformURL, *authURL)
		if err != nil {
			return err
		}
		p, err := c.project(ctx, o.project, nil)
		if err != nil {
			return err
		}
		list, err := c.mem.ListRuns(ctx)
		if err != nil {
			return c.refused(err, memoryTarget{})
		}
		keep := func(i int) bool {
			r := list.Runs[i]
			return p == nil || (r.Scope == memory.ScopeProject && r.Project == p.ID)
		}
		out := cmd.OutOrStdout()
		if o.jsonF {
			raw := list.RawJSON()
			if p != nil {
				if raw, err = filterRaw(raw, "runs", keep); err != nil {
					return err
				}
			}
			return printRawJSON(out, raw)
		}
		var runs []*memory.Run
		for i, r := range list.Runs {
			if keep(i) {
				runs = append(runs, r)
			}
		}
		var names map[string]string
		if p != nil && p.Name != "" {
			names = map[string]string{p.ID: p.Name}
		} else if names, err = c.projectNames(cmd, runs); err != nil {
			return err
		}
		return printTidies(out, runs, names, p)
	}
}

func newMemoryTidiesCmd(platformURL, authURL *string) *cobra.Command {
	var list tidiesList
	cmd := &cobra.Command{
		Use:   "tidies",
		Short: "See what Tidy memory changed each night, and undo, apply or decline it.",
		Long: `See what Tidy memory changed in your memory, and undo it, or apply or decline
what waits for you.

Each night Tidy memory goes through what Latere remembers about you, and
each project with something new. It combines memories that say the same
thing, settles ones that contradict each other, and forgets ones that only
recorded a finished task. A night with nothing new starts nothing. Each
night's work on your memory, or on a project's, is one tidy.

Without a command, list your tidies, the newest first: each one's id, whose
memory it went through, when, where it stands and what it changed.
--project lists one project's, by its name or id.

  latere memory tidies                     your tidies, the newest first
  latere memory tidies show <tidy>         what one changed, before and after
  latere memory tidies undo <tidy>         take back what it applied
  latere memory tidies apply <tidy>        apply what waits for you
  latere memory tidies decline <tidy>      decline what waits for you

Name memories after the tidy to decide only those: 'latere memory tidies
undo <tidy> mia' undoes the change to mia and leaves the rest.

What happens to what Tidy memory changes is your choice, in 'latere memory
settings'. review=apply, the default: its changes apply at once, are listed
here, and you can undo them. review=ask: its changes wait until you apply or
decline them, and left waiting, they lapse and nothing changes. A change to
a memory you wrote waits for your OK either way.`,
		Example: `  latere memory tidies
  latere memory tidies --project "Tide tables"
  latere memory tidies show ses_01JB8K4Q2M6N9P3R5S7T1V3X5Z
  latere memory tidies undo ses_01JB8K4Q2M6N9P3R5S7T1V3X5Z mia
  latere memory settings review=ask`,
		Args: cobra.NoArgs,
		RunE: list.run(platformURL, authURL),
	}
	list.bind(cmd)
	cmd.AddCommand(
		newMemoryTidiesListCmd(platformURL, authURL),
		newMemoryTidiesShowCmd(platformURL, authURL),
		newMemoryTidyDecisionCmd(platformURL, authURL, decideUndo),
		newMemoryTidyDecisionCmd(platformURL, authURL, decideApply),
		newMemoryTidyDecisionCmd(platformURL, authURL, decideDecline),
	)
	return cmd
}

func newMemoryTidiesListCmd(platformURL, authURL *string) *cobra.Command {
	var list tidiesList
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List your tidies, the newest first.",
		Long: `List your tidies, the newest first: each one's id, whose memory it went
through, when it ran, in this computer's time, where it stands and what it
changed. --project lists one project's, by its name or id; --json prints
the platform's answer. 'latere memory tidies' without a command is the same.`,
		Example: `  latere memory tidies list
  latere memory tidies list --project "Tide tables" --json`,
		Args: cobra.NoArgs,
		RunE: list.run(platformURL, authURL),
	}
	list.bind(cmd)
	return cmd
}

func newMemoryTidiesShowCmd(platformURL, authURL *string) *cobra.Command {
	var jsonF bool
	cmd := &cobra.Command{
		Use:   "show <tidy>",
		Short: "Show what one tidy changed, each memory before and after.",
		Long: `Show what one tidy changed: whose memory it went through, when, where it
stands, and each memory it added, updated or removed, with where the change
stands and the memory before and after, a line marked - only before and a
line marked + only after. A change that waits for you says why, and until
when it waits. <tidy> is the id 'latere memory tidies' lists; --json prints
the platform's answer.`,
		Example: `  latere memory tidies show ses_01JB8K4Q2M6N9P3R5S7T1V3X5Z
  latere memory tidies show ses_01JB8K4Q2M6N9P3R5S7T1V3X5Z --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(args[0])
			if id == "" {
				return errors.New("the tidy's id is empty; nothing was sent. `latere memory tidies` lists your tidies")
			}
			c, err := newMemoryClient(*platformURL, *authURL)
			if err != nil {
				return err
			}
			r, err := c.mem.GetRun(cmd.Context(), id)
			if err != nil {
				return c.refused(err, memoryTarget{tidy: id})
			}
			if jsonF {
				return printRawJSON(cmd.OutOrStdout(), r.RawJSON())
			}
			names, err := c.projectNames(cmd, []*memory.Run{r})
			if err != nil {
				return err
			}
			return printTidy(cmd.OutOrStdout(), r, names)
		},
	}
	cmd.Flags().BoolVar(&jsonF, "json", false, "print the platform's answer as JSON")
	return cmd
}
