package commands

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/latere-ai/latere-cli/internal/api"
)

func TestParseSandboxAliases(t *testing.T) {
	aliases, err := parseSandboxAliases([]string{"frontend=my-frontend", "backend=sbx_123"})
	if err != nil {
		t.Fatalf("parseSandboxAliases returned error: %v", err)
	}
	if aliases["frontend"] != "my-frontend" || aliases["backend"] != "sbx_123" {
		t.Fatalf("unexpected aliases: %#v", aliases)
	}
	if _, err := parseSandboxAliases([]string{"broken"}); err == nil {
		t.Fatal("expected malformed alias to fail")
	}
}

func TestSafeToolPaths(t *testing.T) {
	for _, p := range []string{"src/main.go", "./src/../src/main.go"} {
		if _, err := safeToolPath(p); err != nil {
			t.Fatalf("safeToolPath(%q) returned error: %v", p, err)
		}
	}
	for _, p := range []string{"", "/etc/passwd", "../secret", "src/../../secret"} {
		if _, err := safeToolPath(p); err == nil {
			t.Fatalf("safeToolPath(%q) succeeded, want error", p)
		}
	}
	if got, err := safeToolDir("."); err != nil || got != "" {
		t.Fatalf("safeToolDir(.) = %q, %v; want empty nil", got, err)
	}
}

func TestGlobRegexp(t *testing.T) {
	re, err := globRegexp("**/*.go")
	if err != nil {
		t.Fatalf("globRegexp returned error: %v", err)
	}
	for _, p := range []string{"main.go", "internal/commands/mcp.go"} {
		if !re.MatchString(p) {
			t.Fatalf("glob did not match %q", p)
		}
	}
	if re.MatchString("main.ts") {
		t.Fatal("glob matched main.ts, want no match")
	}
}

func TestParseGrepLine(t *testing.T) {
	match := parseGrepLine("internal/commands/mcp.go:42:hello: world")
	if match.Path != "internal/commands/mcp.go" || match.Line != 42 || match.Text != "hello: world" {
		t.Fatalf("unexpected grep match: %#v", match)
	}
}

func TestMCPAgentTools(t *testing.T) {
	fake := newFakeMCPAPI(t)
	fake.sandboxes["demo"] = sandboxDTO{ID: "sbx_demo", Name: "demo", State: "stopped", Tier: "ephemeral", DiskGB: 5, Workdir: "/workspace"}
	fake.sandboxes["sbx_demo"] = fake.sandboxes["demo"]
	fake.files["/workspace/hello.txt"] = []byte("hello world\n")
	fake.files["/workspace/src/main.go"] = []byte("package main\n")
	defer fake.server.Close()

	mt := &mcpTools{
		c:         &api.Client{BaseURL: fake.server.URL, HTTP: fake.server.Client()},
		aliases:   map[string]string{"frontend": "demo"},
		autoStart: true,
	}
	ctx := context.Background()

	if got := mt.aliasFor("sbx_demo", "demo"); got != "frontend" {
		t.Fatalf("aliasFor = %q, want frontend", got)
	}
	if got, err := mt.resolveSandbox("frontend"); err != nil || got != "demo" {
		t.Fatalf("resolveSandbox = %q, %v; want demo nil", got, err)
	}

	_, sandboxes, err := mt.agentSandboxes(ctx, nil, mcpAgentSandboxesArgs{IncludeStopped: true})
	if err != nil {
		t.Fatalf("agentSandboxes returned error: %v", err)
	}
	if len(sandboxes.Sandboxes) == 0 || sandboxes.Sandboxes[0].Alias != "frontend" {
		t.Fatalf("unexpected sandboxes: %#v", sandboxes)
	}
	if _, stoppedFiltered, err := mt.agentSandboxes(ctx, nil, mcpAgentSandboxesArgs{NameFilter: "demo"}); err != nil || len(stoppedFiltered.Sandboxes) != 0 {
		t.Fatalf("agentSandboxes stopped filter = %#v, %v", stoppedFiltered, err)
	}

	_, read, err := mt.agentRead(ctx, nil, mcpReadArgs{Sandbox: "frontend", Path: "hello.txt", Offset: 6, Limit: 5})
	if err != nil {
		t.Fatalf("agentRead returned error: %v", err)
	}
	if read.Content != "world" || !read.Truncated || fake.startCount != 1 {
		t.Fatalf("unexpected read/start state: read=%#v startCount=%d", read, fake.startCount)
	}

	if _, w, err := mt.agentWrite(ctx, nil, mcpWriteArgs{Sandbox: "frontend", Path: "new.txt", Content: "new\n"}); err != nil {
		t.Fatalf("agentWrite returned error: %v", err)
	} else if w.Path != "/workspace/new.txt" {
		t.Fatalf("agentWrite Path = %q, want /workspace/new.txt", w.Path)
	}
	if string(fake.files["/workspace/new.txt"]) != "new\n" {
		t.Fatalf("new.txt = %q", fake.files["/workspace/new.txt"])
	}
	if _, _, err := mt.agentWrite(ctx, nil, mcpWriteArgs{Sandbox: "frontend", Path: "new.txt", Content: "blocked", CreateOnly: true}); err == nil {
		t.Fatal("agentWrite create_only overwrote existing file")
	}

	_, edit, err := mt.agentEdit(ctx, nil, mcpEditArgs{Sandbox: "frontend", Path: "hello.txt", Old: "world", New: "cella"})
	if err != nil {
		t.Fatalf("agentEdit returned error: %v", err)
	}
	if edit.Replacements != 1 || string(fake.files["/workspace/hello.txt"]) != "hello cella\n" {
		t.Fatalf("unexpected edit: %#v file=%q", edit, fake.files["/workspace/hello.txt"])
	}
	if edit.Path != "/workspace/hello.txt" {
		t.Fatalf("agentEdit Path = %q, want /workspace/hello.txt", edit.Path)
	}

	_, bash, err := mt.agentBash(ctx, nil, mcpBashArgs{Sandbox: "frontend", Command: "echo done", Cwd: ".", TimeoutSeconds: 1})
	if err != nil {
		t.Fatalf("agentBash returned error: %v", err)
	}
	if bash.Output != "done\n" || bash.ExitCode == nil || *bash.ExitCode != 0 {
		t.Fatalf("unexpected bash result: %#v", bash)
	}
	if fake.lastCmdCwd != "/workspace" {
		t.Fatalf("agentBash sent cwd=%q, want /workspace", fake.lastCmdCwd)
	}

	_, background, err := mt.agentBash(ctx, nil, mcpBashArgs{Sandbox: "frontend", Command: "sleep 1", Background: true})
	if err != nil {
		t.Fatalf("background agentBash returned error: %v", err)
	}
	_, monitored, err := mt.agentMonitor(ctx, nil, mcpMonitorArgs{Sandbox: "frontend", CommandID: background.CommandID, TimeoutSeconds: 1})
	if err != nil {
		t.Fatalf("agentMonitor returned error: %v", err)
	}
	if len(monitored.Events) == 0 || monitored.Events[0].Text != "done\n" {
		t.Fatalf("unexpected monitor result: %#v", monitored)
	}

	_, glob, err := mt.agentGlob(ctx, nil, mcpGlobArgs{Sandbox: "frontend", Pattern: "**/*.go", Limit: 5})
	if err != nil {
		t.Fatalf("agentGlob returned error: %v", err)
	}
	if len(glob.Matches) != 1 || glob.Matches[0] != "src/main.go" {
		t.Fatalf("unexpected glob result: %#v", glob)
	}

	_, grep, err := mt.agentGrep(ctx, nil, mcpGrepArgs{Sandbox: "frontend", Pattern: "package", Glob: "*.go"})
	if err != nil {
		t.Fatalf("agentGrep returned error: %v", err)
	}
	if len(grep.Matches) != 1 || grep.Matches[0].Path != "src/main.go" {
		t.Fatalf("unexpected grep result: %#v", grep)
	}

	payload := tarFiles(t, map[string]string{"uploaded.txt": "payload\n"})
	_, upload, err := mt.agentUpload(ctx, nil, mcpUploadArgs{Sandbox: "frontend", Dest: "inbox", TarBase64: base64.StdEncoding.EncodeToString(payload)})
	if err != nil {
		t.Fatalf("agentUpload returned error: %v", err)
	}
	if upload.Dest != "/workspace/inbox" || string(fake.files["/workspace/inbox/uploaded.txt"]) != "payload\n" {
		t.Fatalf("unexpected upload result: %#v files=%#v", upload, fake.files)
	}
	// Re-uploading the same archive without overwrite must fail.
	if _, _, err := mt.agentUpload(ctx, nil, mcpUploadArgs{Sandbox: "frontend", Dest: "inbox", TarBase64: base64.StdEncoding.EncodeToString(payload)}); err == nil {
		t.Fatal("agentUpload overwrote existing file with overwrite=false")
	}
	if _, _, err := mt.agentUpload(ctx, nil, mcpUploadArgs{Sandbox: "frontend", Dest: "inbox", Overwrite: true, TarBase64: base64.StdEncoding.EncodeToString(payload)}); err != nil {
		t.Fatalf("agentUpload overwrite=true returned error: %v", err)
	}

	_, download, err := mt.agentDownload(ctx, nil, mcpDownloadArgs{Sandbox: "frontend", Paths: []string{"hello.txt"}})
	if err != nil {
		t.Fatalf("agentDownload returned error: %v", err)
	}
	if download.Bytes == 0 || download.TarBase64 == "" {
		t.Fatalf("unexpected download result: %#v", download)
	}
}

// TestMCPWorkdirRouting locks down the contract that file/shell tools
// resolve paths under the resolved sandbox workdir, regardless of what
// the agent passes as a relative input. The fake server stores files
// under their absolute keys; if any tool slipped relative paths past
// the workdir join, this test would not see the file at all.
func TestMCPWorkdirRouting(t *testing.T) {
	fake := newFakeMCPAPI(t)
	fake.sandboxes["demo"] = sandboxDTO{ID: "demo", Name: "demo", State: "running", Tier: "ephemeral", Workdir: "/srv/agent"}
	fake.files["/srv/agent/notes.txt"] = []byte("hi\n")
	defer fake.server.Close()

	mt := &mcpTools{
		c:         &api.Client{BaseURL: fake.server.URL, HTTP: fake.server.Client()},
		autoStart: true,
	}
	ctx := context.Background()

	_, read, err := mt.agentRead(ctx, nil, mcpReadArgs{Sandbox: "demo", Path: "notes.txt"})
	if err != nil {
		t.Fatalf("agentRead under custom workdir failed: %v", err)
	}
	if read.Path != "/srv/agent/notes.txt" || read.Content != "hi\n" {
		t.Fatalf("read = %#v", read)
	}

	if _, _, err := mt.agentBash(ctx, nil, mcpBashArgs{Sandbox: "demo", Command: "echo done"}); err != nil {
		t.Fatalf("agentBash under custom workdir failed: %v", err)
	}
	if fake.lastCmdCwd != "/srv/agent" {
		t.Fatalf("agentBash sent cwd=%q, want /srv/agent", fake.lastCmdCwd)
	}
	// Verify the export wire body carried an absolute src_dir rooted at
	// the sandbox workdir, not a relative path or the empty default.
	if fake.lastExportDir != "/srv/agent" {
		t.Fatalf("export src_dir on wire = %q, want /srv/agent", fake.lastExportDir)
	}

	if _, _, err := mt.agentWrite(ctx, nil, mcpWriteArgs{Sandbox: "demo", Path: "out.txt", Content: "x"}); err != nil {
		t.Fatalf("agentWrite under custom workdir failed: %v", err)
	}
	if fake.lastImportDest != "/srv/agent" {
		t.Fatalf("import dest on wire = %q, want /srv/agent", fake.lastImportDest)
	}
	if string(fake.files["/srv/agent/out.txt"]) != "x" {
		t.Fatalf("agentWrite did not land under custom workdir: files=%#v", fake.files)
	}
}

// TestMCPUploadRejectsMaliciousTar covers the CLI-side preflight that
// refuses archive entries which would escape the resolved dest. The
// server-side sanitizer is tested separately (sandbox internal/api);
// this nails down that the CLI fails fast before sending the bytes.
func TestMCPUploadRejectsMaliciousTar(t *testing.T) {
	fake := newFakeMCPAPI(t)
	fake.sandboxes["demo"] = sandboxDTO{ID: "demo", Name: "demo", State: "running", Tier: "ephemeral", Workdir: "/workspace"}
	defer fake.server.Close()

	mt := &mcpTools{c: &api.Client{BaseURL: fake.server.URL, HTTP: fake.server.Client()}}
	ctx := context.Background()

	cases := map[string]map[string]string{
		"absolute":      {"/etc/passwd": "x"},
		"parent_escape": {"../escape": "x"},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			payload := tarFiles(t, files)
			_, _, err := mt.agentUpload(ctx, nil, mcpUploadArgs{
				Sandbox:   "demo",
				Dest:      "inbox",
				TarBase64: base64.StdEncoding.EncodeToString(payload),
			})
			if err == nil {
				t.Fatalf("agentUpload accepted %s entry", name)
			}
			if !strings.Contains(err.Error(), "escape") {
				t.Fatalf("error %q does not mention escape", err)
			}
		})
	}
}

// TestMCPEditAmbiguityHasSnippets locks the new structured-edit error
// shape from spec 63: ambiguous matches must surface a line number and
// a short snippet for each occurrence so callers can extend `old`.
func TestMCPEditAmbiguityHasSnippets(t *testing.T) {
	fake := newFakeMCPAPI(t)
	fake.sandboxes["demo"] = sandboxDTO{ID: "demo", Name: "demo", State: "running", Tier: "ephemeral", Workdir: "/workspace"}
	fake.files["/workspace/multi.txt"] = []byte("alpha foo bar\nbeta foo baz\n")
	defer fake.server.Close()

	mt := &mcpTools{c: &api.Client{BaseURL: fake.server.URL, HTTP: fake.server.Client()}}
	ctx := context.Background()

	_, _, err := mt.agentEdit(ctx, nil, mcpEditArgs{Sandbox: "demo", Path: "multi.txt", Old: "foo", New: "qux"})
	if err == nil {
		t.Fatal("agentEdit accepted ambiguous replacement")
	}
	msg := err.Error()
	for _, want := range []string{"matches:", "L1:", "L2:", "alpha", "beta"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error message missing %q: %s", want, msg)
		}
	}
}

// TestMCPDownloadCap exercises the agent download cap. We seed the fake
// to return more bytes than the cap allows and assert the tool refuses
// to base64 the response.
func TestMCPDownloadCap(t *testing.T) {
	fake := newFakeMCPAPI(t)
	fake.sandboxes["demo"] = sandboxDTO{ID: "demo", Name: "demo", State: "running", Tier: "ephemeral", Workdir: "/workspace"}
	// Build a single big file (twice the cap) under the workdir.
	big := bytes.Repeat([]byte("a"), maxAgentDownloadBytes+1)
	fake.files["/workspace/huge.bin"] = big
	defer fake.server.Close()

	mt := &mcpTools{c: &api.Client{BaseURL: fake.server.URL, HTTP: fake.server.Client()}}
	ctx := context.Background()
	_, _, err := mt.agentDownload(ctx, nil, mcpDownloadArgs{Sandbox: "demo", Paths: []string{"huge.bin"}})
	if err == nil || !strings.Contains(err.Error(), "cap") {
		t.Fatalf("agentDownload over cap = %v, want cap error", err)
	}
}

func TestMCPManagementTools(t *testing.T) {
	fake := newFakeMCPAPI(t)
	fake.sandboxes["demo"] = sandboxDTO{ID: "demo", Name: "demo", State: "running", Tier: "ephemeral", Workdir: "/workspace"}
	fake.files["/workspace/artifact.txt"] = []byte("artifact\n")
	defer fake.server.Close()

	mt := &mcpTools{c: &api.Client{BaseURL: fake.server.URL, HTTP: fake.server.Client()}}
	ctx := context.Background()

	if _, got, err := mt.create(ctx, nil, mcpCreateArgs{Name: "created"}); err != nil || got.ID == "" {
		t.Fatalf("create = %#v, %v", got, err)
	}
	if _, got, err := mt.list(ctx, nil, mcpListArgs{}); err != nil || len(got.Sandboxes) == 0 {
		t.Fatalf("list = %#v, %v", got, err)
	}
	if _, got, err := mt.get(ctx, nil, mcpGetArgs{Sandbox: "demo"}); err != nil || got.ID != "demo" {
		t.Fatalf("get = %#v, %v", got, err)
	}
	if _, got, err := mt.start(ctx, nil, mcpVerbArgs{Sandbox: "demo"}); err != nil || got.State != "running" {
		t.Fatalf("start = %#v, %v", got, err)
	}
	if _, got, err := mt.stop(ctx, nil, mcpVerbArgs{Sandbox: "demo"}); err != nil || got.State != "stopped" {
		t.Fatalf("stop = %#v, %v", got, err)
	}
	if _, got, err := mt.extend(ctx, nil, mcpExtendArgs{Sandbox: "demo"}); err != nil || got.Deadline == "" {
		t.Fatalf("extend = %#v, %v", got, err)
	}
	if _, got, err := mt.convert(ctx, nil, mcpConvertArgs{Sandbox: "demo", Tier: "persistent"}); err != nil || got.Tier != "persistent" {
		t.Fatalf("convert = %#v, %v", got, err)
	}
	if _, got, err := mt.convert(ctx, nil, mcpConvertArgs{Sandbox: "demo", Tier: "ephemeral", AutoDeleteHours: 1}); err != nil || got.Tier != "ephemeral" {
		t.Fatalf("convert ephemeral = %#v, %v", got, err)
	}
	if _, _, err := mt.convert(ctx, nil, mcpConvertArgs{Sandbox: "demo", Tier: "ephemeral"}); err == nil {
		t.Fatal("convert accepted ephemeral without auto_delete_hours")
	}
	if _, _, err := mt.convert(ctx, nil, mcpConvertArgs{Sandbox: "demo", Tier: "bad"}); err == nil {
		t.Fatal("convert accepted invalid tier")
	}

	_, run, err := mt.run(ctx, nil, mcpRunArgs{Sandbox: "demo", Argv: []string{"sh", "-lc", "echo done"}})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if _, got, err := mt.wait(ctx, nil, mcpWaitArgs{Sandbox: "demo", CommandID: run.CommandID, TimeoutSeconds: 1}); err != nil || got.Phase != "exited" {
		t.Fatalf("wait = %#v, %v", got, err)
	}
	if _, got, err := mt.logs(ctx, nil, mcpLogsArgs{Sandbox: "demo", CommandID: run.CommandID}); err != nil || got.Bytes == "" {
		t.Fatalf("logs = %#v, %v", got, err)
	}
	if _, got, err := mt.export(ctx, nil, mcpExportArgs{Sandbox: "demo", Paths: []string{"artifact.txt"}}); err != nil || got.Bytes == 0 {
		t.Fatalf("export = %#v, %v", got, err)
	}
	importPayload := base64.StdEncoding.EncodeToString(tarFiles(t, map[string]string{"managed.txt": "ok\n"}))
	if _, got, err := mt.imp(ctx, nil, mcpImportArgs{Sandbox: "demo", TarBase64: importPayload}); err != nil || got.Bytes == 0 {
		t.Fatalf("import = %#v, %v", got, err)
	}
	if _, _, err := mt.imp(ctx, nil, mcpImportArgs{Sandbox: "demo", TarBase64: "bad"}); err == nil {
		t.Fatal("import accepted invalid base64")
	}
	if _, got, err := mt.kill(ctx, nil, mcpKillArgs{Sandbox: "demo", CommandID: run.CommandID}); err != nil || !got.OK {
		t.Fatalf("kill = %#v, %v", got, err)
	}
	if _, got, err := mt.del(ctx, nil, mcpDeleteArgs{Sandbox: "demo"}); err != nil || !got.OK {
		t.Fatalf("delete = %#v, %v", got, err)
	}
}

func TestMCPToolRegistrationAndText(t *testing.T) {
	cmd := newCeMcpCmd()
	if cmd.Use != "mcp" {
		t.Fatalf("unexpected command: %s", cmd.Use)
	}
	if err := cmd.Flags().Set("surface", "management"); err != nil {
		t.Fatalf("set surface: %v", err)
	}
	if got, _ := cmd.Flags().GetString("surface"); got != "management" {
		t.Fatalf("surface flag = %q", got)
	}
	result := mcpText("hello %s", "world")
	if len(result.Content) != 1 {
		t.Fatalf("unexpected MCP text result: %#v", result)
	}
	if err := runMCPServer(context.Background(), &api.Client{}, mcpServerConfig{Surface: "bad"}); err == nil {
		t.Fatal("runMCPServer accepted invalid surface")
	}
}

// TestMCPRegistrationSurfaces enumerates the tools each surface
// registers and asserts the visible name set matches exactly. Drift in
// either direction (a forgotten tool, or a leaked management tool into
// the agent surface) breaks this test.
func TestMCPRegistrationSurfaces(t *testing.T) {
	want := map[string][]string{
		"agent": {
			"Bash", "Download", "Edit", "Glob", "Grep", "Monitor",
			"Read", "Sandboxes", "Upload", "Write",
		},
		"management": {
			"CommandLogs", "ConvertSandbox", "CreateSandbox",
			"DeleteSandbox", "ExportFiles", "ExtendSandbox",
			"GetSandbox", "ImportFiles", "KillCommand",
			"ListSandboxes", "RunCommand", "StartSandbox",
			"StopSandbox", "WaitCommand",
		},
	}
	wantAll := append(append([]string{}, want["agent"]...), want["management"]...)
	sort.Strings(wantAll)
	want["all"] = wantAll

	for _, surface := range []string{"agent", "management", "all"} {
		t.Run(surface, func(t *testing.T) {
			got := registeredToolNames(t, surface)
			if !reflect.DeepEqual(got, want[surface]) {
				t.Fatalf("surface=%s tools = %v\nwant %v", surface, got, want[surface])
			}
		})
	}
}

func registeredToolNames(t *testing.T, surface string) []string {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	mt := &mcpTools{}
	if surface == "agent" || surface == "all" {
		registerAgentTools(srv, mt)
	}
	if surface == "management" || surface == "all" {
		registerManagementTools(srv, mt)
	}
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = ss.Close() }()
	cl := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "test"}, nil)
	cs, err := cl.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = cs.Close() }()
	res, err := cs.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return names
}

func TestMCPErrorPaths(t *testing.T) {
	fake := newFakeMCPAPI(t)
	fake.sandboxes["demo"] = sandboxDTO{ID: "demo", Name: "demo", State: "stopped", Tier: "ephemeral", Workdir: "/workspace"}
	fake.files["/workspace/hello.txt"] = []byte("hello world\n")
	fake.files["/workspace/multi.txt"] = []byte("x x\n")
	fake.files["/workspace/binary.bin"] = []byte{0xff, 0xfe}
	defer fake.server.Close()

	mt := &mcpTools{c: &api.Client{BaseURL: fake.server.URL, HTTP: fake.server.Client()}}
	ctx := context.Background()

	if _, err := mt.resolveSandbox(""); err == nil {
		t.Fatal("resolveSandbox accepted empty selector")
	}
	if _, _, err := mt.ensureSandbox(ctx, "demo"); err == nil {
		t.Fatal("ensureSandbox auto-started with autoStart disabled")
	}
	mt.autoStart = true

	if _, _, err := mt.agentRead(ctx, nil, mcpReadArgs{Sandbox: "demo", Path: "/etc/passwd"}); err == nil {
		t.Fatal("agentRead accepted absolute path")
	}
	if _, _, err := mt.agentRead(ctx, nil, mcpReadArgs{Sandbox: "demo", Path: "binary.bin"}); err == nil {
		t.Fatal("agentRead accepted binary file")
	}
	if _, _, _, err := mt.readTextFile(ctx, "demo", "/workspace", "hello.txt", -1, 1); err == nil {
		t.Fatal("readTextFile accepted negative offset")
	}
	if _, _, _, err := mt.readTextFile(ctx, "demo", "/workspace", "hello.txt", 0, 5<<20+1); err == nil {
		t.Fatal("readTextFile accepted overlarge limit")
	}
	if _, _, _, err := mt.readTextFile(ctx, "demo", "/workspace", "missing.txt", 0, 1); err == nil {
		t.Fatal("readTextFile accepted missing file")
	}

	if _, _, err := mt.agentEdit(ctx, nil, mcpEditArgs{Sandbox: "demo", Path: "hello.txt", Old: "", New: "x"}); err == nil {
		t.Fatal("agentEdit accepted empty old text")
	}
	if _, _, err := mt.agentEdit(ctx, nil, mcpEditArgs{Sandbox: "demo", Path: "hello.txt", Old: "missing", New: "x"}); err == nil {
		t.Fatal("agentEdit accepted missing old text")
	}
	if _, _, err := mt.agentEdit(ctx, nil, mcpEditArgs{Sandbox: "demo", Path: "multi.txt", Old: "x", New: "y"}); err == nil {
		t.Fatal("agentEdit accepted ambiguous replacement")
	}
	if _, got, err := mt.agentEdit(ctx, nil, mcpEditArgs{Sandbox: "demo", Path: "multi.txt", Old: "x", New: "y", ReplaceAll: true}); err != nil || got.Replacements != 2 {
		t.Fatalf("agentEdit replace all = %#v, %v", got, err)
	}

	if _, _, err := mt.agentBash(ctx, nil, mcpBashArgs{Sandbox: "demo", Command: "  "}); err == nil {
		t.Fatal("agentBash accepted empty command")
	}
	if _, _, err := mt.agentBash(ctx, nil, mcpBashArgs{Sandbox: "demo", Command: "echo", Cwd: "../bad"}); err == nil {
		t.Fatal("agentBash accepted escaping cwd")
	}
	if _, _, err := mt.agentGlob(ctx, nil, mcpGlobArgs{Sandbox: "demo", Pattern: ""}); err == nil {
		t.Fatal("agentGlob accepted empty pattern")
	}
	if _, _, err := mt.agentGlob(ctx, nil, mcpGlobArgs{Sandbox: "demo", Pattern: "*", Path: "/root"}); err == nil {
		t.Fatal("agentGlob accepted absolute root")
	}
	if _, _, err := mt.agentGrep(ctx, nil, mcpGrepArgs{Sandbox: "demo", Pattern: "x", Path: "../bad"}); err == nil {
		t.Fatal("agentGrep accepted escaping root")
	}
	if _, _, err := mt.agentUpload(ctx, nil, mcpUploadArgs{Sandbox: "demo", TarBase64: "not-base64"}); err == nil {
		t.Fatal("agentUpload accepted invalid base64")
	}
	if _, _, err := mt.agentUpload(ctx, nil, mcpUploadArgs{Sandbox: "demo", Dest: "../bad", TarBase64: base64.StdEncoding.EncodeToString(tarFiles(t, map[string]string{"x": "y"}))}); err == nil {
		t.Fatal("agentUpload accepted escaping dest")
	}
	if _, _, err := mt.agentDownload(ctx, nil, mcpDownloadArgs{Sandbox: "demo", Paths: []string{"../bad"}}); err == nil {
		t.Fatal("agentDownload accepted escaping path")
	}

	if _, err := tarSingleFile("/bad", []byte("x"), "0644"); err == nil {
		t.Fatal("tarSingleFile accepted absolute path")
	}
	if _, err := tarSingleFile("x", []byte("x"), "not-octal"); err == nil {
		t.Fatal("tarSingleFile accepted invalid mode")
	}
	if _, err := globRegexp(""); err == nil {
		t.Fatal("globRegexp accepted empty pattern")
	}
	if got := parseGrepLine("not-a-grep-line"); got.Path != "" {
		t.Fatalf("parseGrepLine malformed = %#v", got)
	}
	if got := parseGrepLine("file:nope:text"); got.Path != "" {
		t.Fatalf("parseGrepLine bad line number = %#v", got)
	}

	timeoutOut, err := mt.collectCommand(ctx, "demo", fake.addRunningCommand(), 10*time.Millisecond, 3)
	if err != nil {
		t.Fatalf("collectCommand timeout returned error: %v", err)
	}
	if !timeoutOut.TimedOut || !timeoutOut.Truncated {
		t.Fatalf("unexpected timeout result: %#v", timeoutOut)
	}
	quietID := fake.addQuietRunningCommand()
	_, monitored, err := mt.agentMonitor(ctx, nil, mcpMonitorArgs{Sandbox: "demo", CommandID: quietID, TimeoutSeconds: 1})
	if err != nil {
		t.Fatalf("agentMonitor quiet command returned error: %v", err)
	}
	if !monitored.TimedOut {
		t.Fatalf("expected quiet monitor timeout, got %#v", monitored)
	}
}

type fakeMCPAPI struct {
	t          *testing.T
	server     *httptest.Server
	sandboxes  map[string]sandboxDTO
	files      map[string][]byte
	commands   map[string]commandDTO
	logs       map[string]logsCursorDTO
	startCount     int
	nextID         int
	lastCmdCwd     string
	lastImportDest string
	lastExportDir  string
}

func newFakeMCPAPI(t *testing.T) *fakeMCPAPI {
	t.Helper()
	f := &fakeMCPAPI{
		t:         t,
		sandboxes: map[string]sandboxDTO{},
		files:     map[string][]byte{},
		commands:  map[string]commandDTO{},
		logs:      map[string]logsCursorDTO{},
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *fakeMCPAPI) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/v1/sandboxes" {
		switch r.Method {
		case http.MethodGet:
			rows := make([]sandboxDTO, 0, len(f.sandboxes))
			seen := map[string]bool{}
			for _, sb := range f.sandboxes {
				if seen[sb.ID] {
					continue
				}
				seen[sb.ID] = true
				rows = append(rows, sb)
			}
			writeJSON(f.t, w, rows)
		case http.MethodPost:
			f.nextID++
			var req mcpCreateArgs
			_ = json.NewDecoder(r.Body).Decode(&req)
			id := "created"
			if f.nextID > 1 {
				id = id + "-" + string(rune('0'+f.nextID))
			}
			sb := sandboxDTO{ID: id, Name: req.Name, State: "running", Tier: defaultStr(req.Tier, "ephemeral"), DiskGB: req.DiskGB}
			f.sandboxes[id] = sb
			if sb.Name != "" {
				f.sandboxes[sb.Name] = sb
			}
			writeJSON(f.t, w, sb)
		default:
			http.NotFound(w, r)
		}
		return
	}

	rest := strings.TrimPrefix(r.URL.Path, "/v1/sandboxes/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == rest && rest == r.URL.Path {
		http.NotFound(w, r)
		return
	}
	sandbox, err := url.PathUnescape(parts[0])
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			sb, ok := f.sandboxes[sandbox]
			if !ok {
				http.NotFound(w, r)
				return
			}
			writeJSON(f.t, w, sb)
		case http.MethodDelete:
			delete(f.sandboxes, sandbox)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
		return
	}

	switch parts[1] {
	case "start":
		f.startCount++
		sb := f.sandboxes[sandbox]
		sb.State = "running"
		f.sandboxes[sandbox] = sb
		if sb.ID != "" {
			f.sandboxes[sb.ID] = sb
		}
		writeJSON(f.t, w, mcpVerbResult{State: "running"})
	case "stop":
		sb := f.sandboxes[sandbox]
		sb.State = "stopped"
		f.sandboxes[sandbox] = sb
		writeJSON(f.t, w, mcpVerbResult{State: "stopped"})
	case "extend":
		writeJSON(f.t, w, mcpExtendResult{State: "running", Deadline: "2026-05-01T00:00:00Z"})
	case "convert":
		var req mcpConvertArgs
		_ = json.NewDecoder(r.Body).Decode(&req)
		writeJSON(f.t, w, mcpConvertResult{State: "running", Tier: req.Tier})
	case "commands":
		f.handleCommands(w, r, sandbox, parts)
	case "files":
		f.handleFiles(w, r, parts)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeMCPAPI) handleCommands(w http.ResponseWriter, r *http.Request, sandbox string, parts []string) {
	if len(parts) == 2 && r.Method == http.MethodPost {
		var req struct {
			Argv []string `json:"argv"`
			Cwd  string   `json:"cwd"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.lastCmdCwd = req.Cwd
		f.nextID++
		id := "cmd-" + string(rune('0'+f.nextID))
		exit := 0
		cmd := strings.Join(req.Argv, " ")
		output := "done\n"
		if strings.Contains(cmd, "find ") {
			// `find /workspace -type f` returns absolute paths.
			output = "/workspace/hello.txt\n/workspace/src/main.go\n"
		}
		if strings.Contains(cmd, "grep ") {
			output = "/workspace/src/main.go:1:package main\n"
		}
		f.commands[id] = commandDTO{CommandID: id, Phase: "exited", ExitCode: &exit}
		f.logs[id] = logsCursorDTO{Bytes: output, NextCursor: int64(len(output)), Phase: "exited", ExitCode: &exit}
		writeJSON(f.t, w, commandDTO{CommandID: id, Phase: "running"})
		return
	}
	if len(parts) < 3 {
		http.NotFound(w, r)
		return
	}
	cmdID := parts[2]
	if len(parts) == 3 && r.Method == http.MethodGet {
		writeJSON(f.t, w, f.commands[cmdID])
		return
	}
	if len(parts) == 3 && r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(parts) == 4 && parts[3] == "logs" && r.Method == http.MethodGet {
		writeJSON(f.t, w, f.logs[cmdID])
		return
	}
	_ = sandbox
	http.NotFound(w, r)
}

func (f *fakeMCPAPI) addRunningCommand() string {
	f.nextID++
	id := "running-" + string(rune('0'+f.nextID))
	f.commands[id] = commandDTO{CommandID: id, Phase: "running"}
	f.logs[id] = logsCursorDTO{Bytes: "abcdef", NextCursor: 6, Phase: "running"}
	return id
}

func (f *fakeMCPAPI) addQuietRunningCommand() string {
	f.nextID++
	id := "quiet-" + string(rune('0'+f.nextID))
	f.commands[id] = commandDTO{CommandID: id, Phase: "running"}
	f.logs[id] = logsCursorDTO{Phase: "running"}
	return id
}

func (f *fakeMCPAPI) handleFiles(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) != 3 {
		http.NotFound(w, r)
		return
	}
	switch parts[2] {
	case "export":
		var req struct {
			SrcDir string   `json:"src_dir"`
			Paths  []string `json:"paths"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		srcDir := req.SrcDir
		if srcDir == "" {
			srcDir = "/workspace"
		}
		f.lastExportDir = req.SrcDir
		files := map[string]string{}
		if len(req.Paths) == 0 {
			for name, body := range f.files {
				rel := strings.TrimPrefix(name, srcDir+"/")
				if rel != name {
					files[rel] = string(body)
				}
			}
		}
		for _, rel := range req.Paths {
			abs := path.Join(srcDir, rel)
			if body, ok := f.files[abs]; ok {
				files[rel] = string(body)
			}
		}
		w.Header().Set("Content-Type", "application/x-tar")
		_, _ = w.Write(tarFiles(f.t, files))
	case "import":
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		dest := r.FormValue("dest")
		if dest == "" {
			dest = "/workspace"
		}
		f.lastImportDest = r.FormValue("dest")
		file, _, err := r.FormFile("tarball")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer func() { _ = file.Close() }()
		imported, bytesWritten := f.importTar(file, dest)
		writeJSON(f.t, w, mcpImportResult{Imported: imported, Bytes: bytesWritten, Dest: dest})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeMCPAPI) importTar(r io.Reader, dest string) (string, int64) {
	tr := tar.NewReader(r)
	var imported []string
	var bytesWritten int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			f.t.Fatalf("read import tar: %v", err)
		}
		if hdr.FileInfo().IsDir() {
			continue
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			f.t.Fatalf("read import body: %v", err)
		}
		name := path.Join(dest, hdr.Name)
		f.files[name] = body
		imported = append(imported, name)
		bytesWritten += int64(len(body))
	}
	return strings.Join(imported, ","), bytesWritten
}

func tarFiles(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("write tar body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buf.Bytes()
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("write json: %v", err)
	}
}
