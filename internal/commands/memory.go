// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"latere.ai/x/sdk-go/latere"
	"latere.ai/x/sdk-go/memory"

	"github.com/latere-ai/latere-cli/internal/api"
)

// The memory commands manage what Latere remembers about the person and
// their projects through platformd's routes under /v1/account/memory,
// with the SDK's memory package, on the platform's public host, as every
// account call the CLI makes. Every surface manages memory through the
// same routes, so the groups, the bounds and the import prompt are the
// platform's, read from the list, and never restated here.

// memoryCellWidth is the most characters of a memory's text a table shows;
// `show` prints it whole.
const memoryCellWidth = 72

// maxMemoryInput bounds what a command reads for one memory's body or one
// import, from a file or standard input. It is far above what the platform
// takes in either, so a file piped by mistake is not read whole, and the
// platform's own bound is the one a person meets.
const maxMemoryInput = 1 << 20

// memoryGroups are the headings a person reads for each type of memory, as
// every surface names them. The order of the groups is the platform's,
// limits.types; a type the platform adds before this table knows it is
// shown by its type.
var memoryGroups = map[memory.Type]string{
	memory.TypePreference: "Preferences",
	memory.TypeProfile:    "Profile",
	memory.TypeTopic:      "Topics",
	memory.TypeArea:       "Areas",
	memory.TypePerson:     "People",
}

// groupHeading is the heading of a memory's type.
func groupHeading(t memory.Type) string {
	if h, ok := memoryGroups[t]; ok {
		return h
	}
	return string(t)
}

// parseGroup reads what --group was given: a type or its heading, without
// regard to case. Anything else is answered as given, so the platform's
// refusal names the rule and a type it adds is taken before the table
// above knows its heading.
func parseGroup(s string) memory.Type {
	s = strings.TrimSpace(s)
	for t, heading := range memoryGroups {
		if strings.EqualFold(s, string(t)) || strings.EqualFold(s, heading) {
			return t
		}
	}
	return memory.Type(s)
}

// groupHelp is the --group help every command that takes one shares.
const groupHelp = "the memory's group: preference, profile, topic, area or person (or Preferences, Profile, Topics, Areas, People)"

// projectHelp is the --project help of the commands that act on one scope.
const projectHelp = "a project's memory, by the project's name or id, instead of yours"

func newMemoryCmd() *cobra.Command {
	var platformURL, authURL string
	cmd := &cobra.Command{
		Use:   "memory",
		Short: "See and change what Latere remembers about you and your projects.",
		Long: `See and change what Latere remembers about you and your projects.

Latere keeps in mind what it learns in your conversations: how you like to be
answered, who you are, the topics and areas of your life you keep coming back
to, and the people you mention. Each memory is one line, in a group, kept
for you or for one of your projects:

  You         every conversation outside a project reads it, and a project's
              conversations read it too
  a project   only that project's conversations read it

The groups are Preferences, Profile, Topics, Areas and People. Each memory
says where it came from: a conversation, something you added, or an import.

  latere memory list                       what Latere remembers about you
  latere memory add "Keep lists short."    remember something now
  latere memory edit <id> --text "..."     change it
  latere memory mv <id> "Tide tables"      move it to a project
  latere memory rm <id>                    delete it for good
  latere memory restore                    what conversations forgot lately
  latere memory export                     every memory as one zip file
  latere memory import answer.txt          what another assistant remembers
  latere memory settings                   Remember new things and the rest

Memory is kept for your personal account; in an organization's context the
commands are refused, and 'latere org --personal' switches back.

The commands call the platform at https://platform.latere.ai with a token
minted for your login. LATERE_PLATFORM_URL or --platform-url overrides the
address, and LATERE_PLATFORM_TOKEN presents a bearer as given.`,
		Example: `  latere memory list
  latere memory list --project "Tide tables"
  latere memory add "Keep lists under five things."
  latere memory add --group people "Mia is my sister."
  latere memory show keep-lists-under-five-things
  latere memory settings remember=off`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.PersistentFlags().StringVar(&platformURL, "platform-url", "", platformURLUsage)
	cmd.PersistentFlags().StringVar(&authURL, "auth-url", "", "override the auth base URL (default $AUTH_URL or derived from the platform URL)")
	cmd.AddCommand(
		newMemoryListCmd(&platformURL, &authURL),
		newMemoryShowCmd(&platformURL, &authURL),
		newMemoryAddCmd(&platformURL, &authURL),
		newMemoryEditCmd(&platformURL, &authURL),
		newMemoryMoveCmd(&platformURL, &authURL),
		newMemoryRemoveCmd(&platformURL, &authURL),
		newMemoryForgetCmd(&platformURL, &authURL),
		newMemoryRestoreCmd(&platformURL, &authURL),
		newMemoryExportCmd(&platformURL, &authURL),
		newMemoryImportCmd(&platformURL, &authURL),
		newMemorySettingsCmd(&platformURL, &authURL),
	)
	return cmd
}

// ---- the client ----

// memoryClient is one command's connection to the memory routes: the SDK's
// memory client, and the platform's address as resolved, which messages
// name.
type memoryClient struct {
	base string
	mem  *memory.Client
}

// newMemoryClient resolves the platform's address as the repository
// commands do, and its bearer: LATERE_PLATFORM_TOKEN as given, else an
// actor token minted for the platform's audience from the saved login at
// the first request.
func newMemoryClient(platformURL, authURL string) (*memoryClient, error) {
	base := resolvePlatformURL(platformURL)
	cfg, err := api.Product{
		BaseURL: base, AuthURL: authURL, Audience: latere.DefaultAudience,
		Token: strings.TrimSpace(os.Getenv("LATERE_PLATFORM_TOKEN")), Timeout: platformTimeout,
	}.Config()
	if err != nil {
		return nil, err
	}
	return &memoryClient{base: base, mem: memory.New(cfg)}, nil
}

// refused is what a command says of an error a memory call answered, as
// the repository commands word a refusal of the platform, with what each
// memory code names put into its sentence.
func (c *memoryClient) refused(err error, about memoryTarget) error {
	return wrapReposErr(explainMemoryRefusal(platformRefused(c.base, err), about))
}

// memoryTarget is what a refusal is about: the memory's id, "" for none,
// and its scope.
type memoryTarget struct {
	id      string
	project *memoryProject
	// restore says the call restored a forgotten memory, whose not-found
	// means nothing forgotten has the id.
	restore bool
}

// explainMemoryRefusal puts what a refusal of a memory call names into its
// sentence: the memory and the scope it was looked for in, and what to run
// next.
func explainMemoryRefusal(err error, about memoryTarget) error {
	e, ok := errors.AsType[*platformError](err)
	if !ok {
		return err
	}
	where := about.project.label()
	switch e.Code {
	case "memory_not_found":
		switch {
		case about.restore:
			e.Message = fmt.Sprintf("Nothing forgotten in %s has the id %s. `latere memory restore` lists what can be restored.", where, about.id)
		case about.id != "":
			e.Message = fmt.Sprintf("There is no memory %s in %s. `latere memory list%s` shows the ids.", about.id, where, about.project.flag())
		}
	case "not_found":
		if about.project != nil {
			e.Message = fmt.Sprintf("You have no project %s. `latere memory list` names your projects.", about.project.ID)
		}
	case "memory_taken":
		e.Message = fmt.Sprintf("A memory with the id %s is in %s again. Rename or delete it, then restore this one.", about.id, where)
	case "memory_personal_only":
		e.Message += " `latere org --personal` switches to your personal account."
	}
	return e
}

// ---- projects ----

// memoryProject is a project whose memory a command acts on: its id, and
// its name when the command read it.
type memoryProject struct {
	ID, Name string
}

// label names a scope for a person: You, or the project.
func (p *memoryProject) label() string {
	switch {
	case p == nil:
		return "You"
	case p.Name != "":
		return fmt.Sprintf("the project %s (%s)", p.Name, p.ID)
	default:
		return "the project " + p.ID
	}
}

// flag is the --project flag that names the project in a command line,
// "" for You.
func (p *memoryProject) flag() string {
	if p == nil {
		return ""
	}
	return " --project " + p.ID
}

// ref addresses the memory id in the scope.
func (p *memoryProject) ref(id string) memory.Ref {
	if p == nil {
		return memory.Ref{Scope: memory.ScopeYou, ID: id}
	}
	return memory.Ref{Scope: memory.ScopeProject, Project: p.ID, ID: id}
}

// scope is the scope and project a write names for the scope.
func (p *memoryProject) scope() (memory.Scope, string) {
	if p == nil {
		return memory.ScopeYou, ""
	}
	return memory.ScopeProject, p.ID
}

// projectIDPrefix starts every project's id, which a project named by its
// id is told apart by without reading the list.
const projectIDPrefix = "prj_"

// project resolves what --project or a destination names: nil for "", a
// project's id as given, or a project's name, read from list, or from the
// platform's list when list is nil. A name two projects share is refused
// with both ids.
func (c *memoryClient) project(ctx context.Context, arg string, list *memory.List) (*memoryProject, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return nil, nil
	}
	if list == nil && strings.HasPrefix(arg, projectIDPrefix) {
		return &memoryProject{ID: arg}, nil
	}
	if list == nil {
		var err error
		if list, err = c.mem.ListMemories(ctx); err != nil {
			return nil, c.refused(err, memoryTarget{})
		}
	}
	return projectIn(list, arg)
}

// projectIn finds the project arg names among the list's projects, by id or
// by name without regard to case. An id the list does not name is taken as
// given, so the platform answers for it.
func projectIn(list *memory.List, arg string) (*memoryProject, error) {
	var found []memory.Project
	for _, p := range list.Projects {
		if p.ID == arg {
			return &memoryProject{ID: p.ID, Name: p.Name}, nil
		}
		if strings.EqualFold(strings.TrimSpace(p.Name), arg) {
			found = append(found, p)
		}
	}
	switch len(found) {
	case 0:
		if strings.HasPrefix(arg, projectIDPrefix) {
			return &memoryProject{ID: arg}, nil
		}
		return nil, fmt.Errorf("you have no project named %q; nothing was sent. `latere memory list` names your projects", arg)
	case 1:
		return &memoryProject{ID: found[0].ID, Name: found[0].Name}, nil
	default:
		ids := make([]string, len(found))
		for i, p := range found {
			ids[i] = p.ID
		}
		return nil, fmt.Errorf("%d of your projects are named %q (%s); nothing was sent. Name the one you mean by its id", len(found), arg, strings.Join(ids, ", "))
	}
}

// ---- what is printed ----

// clean drops what a terminal would read as an instruction rather than
// text, a control character such as an escape, from what a conversation
// or an import wrote; keepLines keeps line breaks and tabs, for a body.
func clean(s string, keepLines bool) string {
	return strings.Map(func(r rune) rune {
		if keepLines && (r == '\n' || r == '\t') {
			return r
		}
		if unicode.IsControl(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, s)
}

// cell is a memory's text as a table shows it: one line, cut at
// memoryCellWidth characters.
func cell(s string) string {
	s = oneLine(clean(s, false))
	if utf8.RuneCountInString(s) <= memoryCellWidth {
		return s
	}
	r := []rune(s)
	return string(r[:memoryCellWidth-1]) + "…"
}

// memorySource says where a memory came from, for a person.
func memorySource(source string) string {
	switch source {
	case "":
		return "not known"
	case memory.SourceYou:
		return "you"
	case memory.SourceImport:
		return "an import"
	default:
		return "the conversation " + clean(source, false)
	}
}

// sourceCell is where a memory came from, as a table shows it.
func sourceCell(source string) string {
	return defaultStr(clean(source, false), "-")
}

// scopeOf names the scope a memory is in, with the project's name when the
// command read it.
func scopeOf(m *memory.Memory, p *memoryProject) string {
	if m.Scope != memory.ScopeProject {
		return "You"
	}
	if p != nil && p.ID == m.Project && p.Name != "" {
		return fmt.Sprintf("%s (%s)", p.Name, m.Project)
	}
	return "the project " + m.Project
}

// printMemory writes one memory as a record, its body after it.
func printMemory(out io.Writer, m *memory.Memory, p *memoryProject) error {
	var b strings.Builder
	b.WriteString(formatWrappedField("id", m.ID))
	b.WriteString(formatWrappedField("name", clean(m.Name, false)))
	b.WriteString(formatWrappedField("memory", clean(m.Description, false)))
	b.WriteString(formatWrappedField("group", groupHeading(m.Type)))
	b.WriteString(formatWrappedField("in", scopeOf(m, p)))
	b.WriteString(formatWrappedField("from", memorySource(m.Source)))
	if m.Sensitive {
		b.WriteString(formatWrappedField("sensitive", "yes, kept while Include sensitive topics is on"))
	}
	if !m.Updated.IsZero() {
		b.WriteString(formatWrappedField("updated", m.Updated.UTC().Format(utcMinute)))
	}
	if body := strings.TrimRight(clean(m.Body, true), "\n"); body != "" {
		b.WriteString("\n" + body + "\n")
	}
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("write the memory: %w", err)
	}
	return nil
}

// groupOrder is the order memories are listed in: the platform's types,
// then any type a memory has that the platform did not list, in the order
// first met.
func groupOrder(types []memory.Type, ms []*memory.Memory) []memory.Type {
	order := append([]memory.Type(nil), types...)
	seen := map[memory.Type]bool{}
	for _, t := range types {
		seen[t] = true
	}
	for _, m := range ms {
		if !seen[m.Type] {
			seen[m.Type] = true
			order = append(order, m.Type)
		}
	}
	return order
}

// inScope reports whether m is in the scope p names, You for nil.
func inScope(m *memory.Memory, p *memoryProject) bool {
	if p == nil {
		return m.Scope != memory.ScopeProject
	}
	return m.Scope == memory.ScopeProject && m.Project == p.ID
}

// writeMemoryTable writes ms as one table, by group in order.
func writeMemoryTable(b io.Writer, ms []*memory.Memory, order []memory.Type) {
	w := tabwriter.NewWriter(b, 0, 4, 2, ' ', 0)
	fprintln(w, "GROUP\tID\tMEMORY\tUPDATED\tFROM")
	for _, t := range order {
		for _, m := range ms {
			if m.Type != t {
				continue
			}
			text := cell(m.Description)
			if m.Sensitive {
				text += " (sensitive)"
			}
			fprintf(w, "%s\t%s\t%s\t%s\t%s\n", groupHeading(t), m.ID, text, humanAge(m.Updated), sourceCell(m.Source))
		}
	}
	_ = w.Flush()
}

// printMemoryList writes the memories of the scope p names by group, and
// for You a line per project with memory after them.
func printMemoryList(out io.Writer, list *memory.List, p *memoryProject) error {
	var b strings.Builder
	var ms []*memory.Memory
	for _, m := range list.Memories {
		if inScope(m, p) {
			ms = append(ms, m)
		}
	}
	switch {
	case len(ms) > 0:
		writeMemoryTable(&b, ms, groupOrder(list.Limits.Types, ms))
	case p == nil:
		b.WriteString("Latere remembers nothing about you yet. Conversations save what is worth keeping, and `latere memory add <text>` saves something now.\n")
	default:
		fmt.Fprintf(&b, "Latere remembers nothing for %s yet. `latere memory add%s <text>` saves something now.\n", p.label(), p.flag())
	}
	if p == nil {
		writeProjectsWithMemory(&b, list)
	}
	if list.More {
		b.WriteString("\nYour memory holds more files than one list reads, so some are not shown.\n")
	}
	if !list.Settings.Remember {
		b.WriteString("\nRemember new things is off: conversations use what is remembered and save nothing new. `latere memory settings remember=on` turns it on.\n")
	}
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("write the memory list: %w", err)
	}
	return nil
}

// writeProjectsWithMemory writes a line per project that has memory, in
// the order the platform names the projects, then any project a memory
// names that the list of projects does not.
func writeProjectsWithMemory(b *strings.Builder, list *memory.List) {
	count := map[string]int{}
	var order []string
	for _, m := range list.Memories {
		if m.Scope != memory.ScopeProject {
			continue
		}
		if count[m.Project] == 0 {
			order = append(order, m.Project)
		}
		count[m.Project]++
	}
	if len(order) == 0 {
		return
	}
	names := map[string]string{}
	var ids []string
	for _, p := range list.Projects {
		names[p.ID] = p.Name
		if count[p.ID] > 0 {
			ids = append(ids, p.ID)
		}
	}
	for _, id := range order {
		if _, named := names[id]; !named {
			ids = append(ids, id)
		}
	}
	b.WriteString("\nProjects:\n")
	w := tabwriter.NewWriter(b, 0, 4, 2, ' ', 0)
	fprintln(w, "PROJECT\tID\tMEMORIES")
	for _, id := range ids {
		fprintf(w, "%s\t%s\t%d\n", defaultStr(clean(names[id], false), "-"), id, count[id])
	}
	_ = w.Flush()
	b.WriteString("`latere memory list --project <name or id>` lists a project's memories.\n")
}

// filterRaw answers raw, a JSON object, with the array under key kept to
// the elements keep accepts, so --json of one project keeps the shape of
// the platform's answer.
func filterRaw(raw json.RawMessage, key string, keep func(i int) bool) (json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("parse the platform's answer: %w", err)
	}
	var items []json.RawMessage
	if len(obj[key]) > 0 {
		if err := json.Unmarshal(obj[key], &items); err != nil {
			return nil, fmt.Errorf("parse the platform's answer: %w", err)
		}
	}
	kept := []json.RawMessage{}
	for i, item := range items {
		if keep(i) {
			kept = append(kept, item)
		}
	}
	b, err := json.Marshal(kept)
	if err != nil {
		return nil, err
	}
	obj[key] = b
	return json.Marshal(obj)
}

// plural is n and the word, with its plural when n is not one.
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// ---- the commands ----

func newMemoryListCmd(platformURL, authURL *string) *cobra.Command {
	var (
		project string
		jsonF   bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List what Latere remembers about you, or about a project.",
		Long: `List what Latere remembers about you, by group, in one table: each memory's
id, its text, when it was last saved and where it came from (you, an import,
or the id of the conversation that saved it). A memory's text is cut to fit
the table; 'latere memory show <id>' prints it whole.

After your own memories come the projects that have memory, one line each.
--project lists one project's memories instead, by the project's name or its
id. --json prints the platform's answer, every memory of every project with
its settings, or with --project that project's memories alone.`,
		Example: `  latere memory list
  latere memory list --project "Tide tables"
  latere memory list --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, err := newMemoryClient(*platformURL, *authURL)
			if err != nil {
				return err
			}
			list, err := c.mem.ListMemories(ctx)
			if err != nil {
				return c.refused(err, memoryTarget{})
			}
			p, err := c.project(ctx, project, list)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if !jsonF {
				return printMemoryList(out, list, p)
			}
			if p == nil {
				return printRawJSON(out, list.RawJSON())
			}
			raw, err := filterRaw(list.RawJSON(), "memories", func(i int) bool { return inScope(list.Memories[i], p) })
			if err != nil {
				return err
			}
			if raw, err = filterRaw(raw, "projects", func(i int) bool { return list.Projects[i].ID == p.ID }); err != nil {
				return err
			}
			return printRawJSON(out, raw)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "list a project's memories, by the project's name or id")
	cmd.Flags().BoolVar(&jsonF, "json", false, "print the platform's answer as JSON")
	return cmd
}

func newMemoryShowCmd(platformURL, authURL *string) *cobra.Command {
	var (
		project string
		jsonF   bool
	)
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show one memory, with its detail.",
		Long: `Show one memory: its name, its text, its group, whose it is, where it came
from, and the detail kept with it. <id> is the memory's id as 'latere memory
list' shows it; --project names the project whose memory it is.`,
		Example: `  latere memory show keep-lists-under-five-things
  latere memory show audience --project "Tide tables"
  latere memory show audience --project prj_01JA4Q0Z8X3K5V7M9N1P2R4T6W --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := newMemoryClient(*platformURL, *authURL)
			if err != nil {
				return err
			}
			p, err := c.project(ctx, project, nil)
			if err != nil {
				return err
			}
			m, err := c.mem.GetMemory(ctx, p.ref(args[0]))
			if err != nil {
				return c.refused(err, memoryTarget{id: args[0], project: p})
			}
			if jsonF {
				return printRawJSON(cmd.OutOrStdout(), m.RawJSON())
			}
			return printMemory(cmd.OutOrStdout(), m, p)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", projectHelp)
	cmd.Flags().BoolVar(&jsonF, "json", false, "print the memory as JSON")
	return cmd
}

// memoryBody is the body --body gave: the text, or standard input for "-".
func memoryBody(cmd *cobra.Command, body string) (string, error) {
	if body != "-" {
		return body, nil
	}
	return readMemoryInput(cmd.InOrStdin(), "standard input")
}

// readMemoryInput reads in whole, up to maxMemoryInput; what names it in a
// refusal.
func readMemoryInput(in io.Reader, what string) (string, error) {
	b, err := io.ReadAll(io.LimitReader(in, maxMemoryInput+1))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", what, err)
	}
	if len(b) > maxMemoryInput {
		return "", fmt.Errorf("%s holds more than %d bytes, far more than memory takes; nothing was sent", what, maxMemoryInput)
	}
	return string(b), nil
}

func newMemoryAddCmd(platformURL, authURL *string) *cobra.Command {
	var (
		group, name, body, project string
		jsonF                      bool
	)
	cmd := &cobra.Command{
		Use:   "add <text>",
		Short: "Remember something now.",
		Long: `Remember something now: <text> is saved as you typed it, as one memory, and
every conversation started from then on knows it. The words after add are
joined with spaces, so quotes are needed only around characters your shell
reads.

The memory goes under Preferences unless --group names another group, and
into your own memory unless --project names one of your projects. Its name
is made from its first words unless --name gives one, and its id from its
name. --body keeps more detail with it, - for standard input.`,
		Example: `  latere memory add "Keep lists under five things."
  latere memory add --group people "Mia is my sister. Her birthday is November 22."
  latere memory add --project "Tide tables" --group topic "The audience is sailors."
  latere memory add --name "Short lists" --body - "Keep lists short." < detail.md`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			text := strings.TrimSpace(strings.Join(args, " "))
			if text == "" {
				return errors.New("the memory's text is empty; nothing was saved")
			}
			detail, err := memoryBody(cmd, body)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			c, err := newMemoryClient(*platformURL, *authURL)
			if err != nil {
				return err
			}
			p, err := c.project(ctx, project, nil)
			if err != nil {
				return err
			}
			scope, projectID := p.scope()
			m, err := c.mem.AddMemory(ctx, &memory.AddMemoryParams{
				Scope: scope, Project: projectID, Type: parseGroup(group),
				Name: strings.TrimSpace(name), Description: text, Body: detail,
			})
			if err != nil {
				return c.refused(err, memoryTarget{project: p})
			}
			if jsonF {
				return printRawJSON(cmd.OutOrStdout(), m.RawJSON())
			}
			return printMemory(cmd.OutOrStdout(), m, p)
		},
	}
	f := cmd.Flags()
	f.StringVar(&group, "group", "", groupHelp+"; Preferences when left out")
	f.StringVar(&name, "name", "", "a title of a few words; made from the text's first words when left out")
	f.StringVar(&body, "body", "", "more detail to keep with the memory, - for standard input")
	f.StringVar(&project, "project", "", "add to a project's memory, by the project's name or id, instead of yours")
	f.BoolVar(&jsonF, "json", false, "print the memory as JSON")
	return cmd
}

func newMemoryEditCmd(platformURL, authURL *string) *cobra.Command {
	var (
		text, name, group, body, project string
		jsonF                            bool
	)
	cmd := &cobra.Command{
		Use:   "edit <id>",
		Short: "Change a memory's text, name, group or detail.",
		Long: `Change a memory: --text its text, --name its name, --group its group, --body
the detail kept with it (- for standard input, "" to remove it). What no
flag names stays as it is. The id stays the same; 'latere memory mv' moves
a memory to You or a project.`,
		Example: `  latere memory edit keep-lists-under-five-things --text "Keep lists under four things."
  latere memory edit mia --group people --body "Lives in Lisbon."
  latere memory edit audience --project "Tide tables" --body ""`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cmd.Flags()
			params := &memory.UpdateMemoryParams{}
			if flags.Changed("text") {
				if strings.TrimSpace(text) == "" {
					return errors.New("a memory's text cannot be empty; nothing was sent. `latere memory rm` deletes a memory")
				}
				params.Description = &text
			}
			if flags.Changed("name") {
				params.Name = &name
			}
			if flags.Changed("group") {
				if params.Type = parseGroup(group); params.Type == "" {
					return errors.New("--group is empty; nothing was sent")
				}
			}
			if flags.Changed("body") {
				detail, err := memoryBody(cmd, body)
				if err != nil {
					return err
				}
				params.Body = &detail
			}
			if params.Description == nil && params.Name == nil && params.Type == "" && params.Body == nil {
				return errors.New("name what to change with --text, --name, --group or --body; nothing was sent")
			}
			ctx := cmd.Context()
			c, err := newMemoryClient(*platformURL, *authURL)
			if err != nil {
				return err
			}
			p, err := c.project(ctx, project, nil)
			if err != nil {
				return err
			}
			m, err := c.mem.UpdateMemory(ctx, p.ref(args[0]), params)
			if err != nil {
				return c.refused(err, memoryTarget{id: args[0], project: p})
			}
			if jsonF {
				return printRawJSON(cmd.OutOrStdout(), m.RawJSON())
			}
			return printMemory(cmd.OutOrStdout(), m, p)
		},
	}
	f := cmd.Flags()
	f.StringVar(&text, "text", "", "the memory's new text, one line")
	f.StringVar(&name, "name", "", "the memory's new name")
	f.StringVar(&group, "group", "", groupHelp)
	f.StringVar(&body, "body", "", `the new detail, - for standard input, "" to remove it`)
	f.StringVar(&project, "project", "", projectHelp)
	f.BoolVar(&jsonF, "json", false, "print the memory as JSON")
	return cmd
}

// youScope is the destination of a move that names the person's own
// memory.
const youScope = "you"

func newMemoryMoveCmd(platformURL, authURL *string) *cobra.Command {
	var (
		project string
		jsonF   bool
	)
	cmd := &cobra.Command{
		Use:   "mv <id> <you|project>",
		Short: "Move a memory to You or to a project.",
		Long: `Move a memory to your own memory, which every conversation reads, with you,
or to one of your projects, by its name or id, where only that project's
conversations read it. --project names the project the memory is in now;
without it the memory is one of your own.

A move keeps the memory's id when the destination has none like it, and
otherwise gives it the next one free there; the command prints the id it
has now.`,
		Example: `  latere memory mv audience "Tide tables"
  latere memory mv audience you --project "Tide tables"
  latere memory mv audience prj_01JA4Q0Z8X3K5V7M9N1P2R4T6W --project "Tide tables"`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, dest := args[0], strings.TrimSpace(args[1])
			ctx := cmd.Context()
			c, err := newMemoryClient(*platformURL, *authURL)
			if err != nil {
				return err
			}
			// A project named by its name is read from the list, once for
			// both sides of the move.
			byName := func(arg string) bool { return arg != "" && !strings.HasPrefix(arg, projectIDPrefix) }
			var list *memory.List
			if byName(strings.TrimSpace(project)) || (!strings.EqualFold(dest, youScope) && byName(dest)) {
				if list, err = c.mem.ListMemories(ctx); err != nil {
					return c.refused(err, memoryTarget{})
				}
			}
			from, err := c.project(ctx, project, list)
			if err != nil {
				return err
			}
			var to *memoryProject
			if !strings.EqualFold(dest, youScope) {
				if dest == "" {
					return errors.New("the destination is empty: name you or a project; nothing was sent")
				}
				if to, err = c.project(ctx, dest, list); err != nil {
					return err
				}
			}
			if (from == nil && to == nil) || (from != nil && to != nil && from.ID == to.ID) {
				fprintf(cmd.OutOrStdout(), "%s is in %s already, so nothing changed.\n", id, to.label())
				return nil
			}
			scope, projectID := to.scope()
			m, err := c.mem.UpdateMemory(ctx, from.ref(id), &memory.UpdateMemoryParams{Scope: scope, Project: projectID})
			if err != nil {
				return c.refused(err, memoryTarget{id: id, project: from})
			}
			if jsonF {
				return printRawJSON(cmd.OutOrStdout(), m.RawJSON())
			}
			as := ""
			if m.ID != id {
				as = ", as " + m.ID
			}
			fprintf(cmd.OutOrStdout(), "Moved %s from %s to %s%s.\n", id, from.label(), to.label(), as)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "the project the memory is in now, by its name or id")
	cmd.Flags().BoolVar(&jsonF, "json", false, "print the memory as JSON")
	return cmd
}

// yesTyped reports whether line answers a [y/N] question with yes.
func yesTyped(line string) bool {
	return strings.EqualFold(line, "y") || strings.EqualFold(line, "yes")
}

func newMemoryRemoveCmd(platformURL, authURL *string) *cobra.Command {
	var (
		project string
		yes     bool
	)
	cmd := &cobra.Command{
		Use:   "rm <id>",
		Short: "Delete a memory for good.",
		Long: `Delete a memory for good. A memory you delete is gone at once and is not kept
among what was recently forgotten, so 'latere memory restore' cannot bring
it back. The command asks first unless --yes is given.

To keep the memory and stop a conversation from relying on it, change it
with 'latere memory edit' instead.`,
		Example: `  latere memory rm keep-lists-under-five-things
  latere memory rm audience --project "Tide tables" --yes`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			ctx := cmd.Context()
			c, err := newMemoryClient(*platformURL, *authURL)
			if err != nil {
				return err
			}
			p, err := c.project(ctx, project, nil)
			if err != nil {
				return err
			}
			if !yes {
				fprintf(cmd.ErrOrStderr(), "Delete %s from %s for good? It cannot be restored. [y/N] ", id, p.label())
				line, err := readLine(cmd.InOrStdin())
				if err != nil {
					return fmt.Errorf("read the answer: %w", err)
				}
				if !yesTyped(line) {
					fprintln(cmd.ErrOrStderr())
					return errors.New("nothing was deleted. Pass --yes to delete without asking")
				}
			}
			if err := c.mem.DeleteMemory(ctx, p.ref(id), nil); err != nil {
				return c.refused(err, memoryTarget{id: id, project: p})
			}
			fprintf(cmd.OutOrStdout(), "Deleted %s from %s.\n", id, p.label())
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", projectHelp)
	cmd.Flags().BoolVar(&yes, "yes", false, "delete without asking")
	return cmd
}

func newMemoryForgetCmd(platformURL, authURL *string) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "forget",
		Short: "Forget everything Latere remembers about you and your projects.",
		Long: `Forget everything: every memory of yours and of every one of your projects
is deleted for good, with what was recently forgotten, so nothing can be
restored. Your settings stay as they are, so conversations go on saving new
memories while Remember new things is on.

The command asks you to type yes unless --yes is given. 'latere memory
export' first keeps a copy.`,
		Example: `  latere memory export && latere memory forget
  latere memory forget --yes`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !yes {
				errOut := cmd.ErrOrStderr()
				fprint(errOut, "This deletes every memory of yours and of your projects for good, with what was recently forgotten.\nType yes to forget everything: ")
				line, err := readLine(cmd.InOrStdin())
				if err != nil {
					return fmt.Errorf("read the answer: %w", err)
				}
				if !strings.EqualFold(line, "yes") {
					fprintln(errOut)
					return errors.New("nothing was deleted: forgetting everything takes yes, typed in full. Pass --yes to forget without asking")
				}
			}
			c, err := newMemoryClient(*platformURL, *authURL)
			if err != nil {
				return err
			}
			if err := c.mem.ForgetEverything(cmd.Context()); err != nil {
				return c.refused(err, memoryTarget{})
			}
			fprintln(cmd.OutOrStdout(), "Forgot everything Latere remembered about you and your projects.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "forget everything without asking")
	return cmd
}

// forgottenDay is how the day a forgotten memory is gone for good is shown.
const forgottenDay = "2006-01-02"

func newMemoryRestoreCmd(platformURL, authURL *string) *cobra.Command {
	var (
		project string
		jsonF   bool
	)
	cmd := &cobra.Command{
		Use:   "restore [id]",
		Short: "List what conversations forgot lately, or bring one back.",
		Long: `Without an id, list what conversations forgot in the last 30 days, the one
forgotten last first, each with the day it is gone for good. With an id,
bring that memory back where it was; --project names the project it was
forgotten from.

A memory you deleted yourself, with 'latere memory rm' or 'latere memory
forget', is gone for good and is not listed. A restore is refused when a
memory with the same id was saved since: rename or delete that one first.`,
		Example: `  latere memory restore
  latere memory restore trains
  latere memory restore audience --project "Tide tables"`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := newMemoryClient(*platformURL, *authURL)
			if err != nil {
				return err
			}
			p, err := c.project(ctx, project, nil)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(args) == 1 {
				m, err := c.mem.RestoreMemory(ctx, p.ref(args[0]))
				if err != nil {
					return c.refused(err, memoryTarget{id: args[0], project: p, restore: true})
				}
				if jsonF {
					return printRawJSON(out, m.RawJSON())
				}
				return printMemory(out, m, p)
			}
			list, err := c.mem.ListForgotten(ctx)
			if err != nil {
				return c.refused(err, memoryTarget{})
			}
			keep := func(i int) bool {
				f := list.Forgotten[i]
				return p == nil || (f.Scope == memory.ScopeProject && f.Project == p.ID)
			}
			if jsonF {
				raw := list.RawJSON()
				if p != nil {
					if raw, err = filterRaw(raw, "forgotten", keep); err != nil {
						return err
					}
				}
				return printRawJSON(out, raw)
			}
			return printForgotten(out, list, keep, p)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "a project's forgotten memories, by the project's name or id")
	cmd.Flags().BoolVar(&jsonF, "json", false, "print the platform's answer as JSON")
	return cmd
}

// printForgotten writes what was forgotten and keep accepts, as a table.
func printForgotten(out io.Writer, list *memory.ForgottenList, keep func(int) bool, p *memoryProject) error {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	n := 0
	for i, f := range list.Forgotten {
		if !keep(i) {
			continue
		}
		if n == 0 {
			fprintln(w, "IN\tID\tFORGOTTEN\tGONE FOR GOOD ON")
		}
		n++
		in := "You"
		if f.Scope == memory.ScopeProject {
			in = f.Project
			if p != nil && p.Name != "" {
				in = p.Name
			}
		}
		fprintf(w, "%s\t%s\t%s\t%s\n", in, f.ID, humanAge(f.ForgottenAt), f.PurgesAt.UTC().Format(forgottenDay))
	}
	_ = w.Flush()
	switch {
	case n == 0 && p == nil:
		b.WriteString("Nothing was forgotten in the last 30 days.\n")
	case n == 0:
		fmt.Fprintf(&b, "Nothing was forgotten in %s in the last 30 days.\n", p.label())
	default:
		b.WriteString("`latere memory restore <id>` brings one back, with --project for a project's.\n")
	}
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("write what was forgotten: %w", err)
	}
	return nil
}
