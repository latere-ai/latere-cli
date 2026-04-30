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
	fake.sandboxes["demo"] = sandboxDTO{ID: "sbx_demo", Name: "demo", State: "stopped", Tier: "ephemeral", DiskGB: 5}
	fake.sandboxes["sbx_demo"] = fake.sandboxes["demo"]
	fake.files["hello.txt"] = []byte("hello world\n")
	fake.files["src/main.go"] = []byte("package main\n")
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

	if _, _, err := mt.agentWrite(ctx, nil, mcpWriteArgs{Sandbox: "frontend", Path: "new.txt", Content: "new\n"}); err != nil {
		t.Fatalf("agentWrite returned error: %v", err)
	}
	if string(fake.files["new.txt"]) != "new\n" {
		t.Fatalf("new.txt = %q", fake.files["new.txt"])
	}
	if _, _, err := mt.agentWrite(ctx, nil, mcpWriteArgs{Sandbox: "frontend", Path: "new.txt", Content: "blocked", CreateOnly: true}); err == nil {
		t.Fatal("agentWrite create_only overwrote existing file")
	}

	_, edit, err := mt.agentEdit(ctx, nil, mcpEditArgs{Sandbox: "frontend", Path: "hello.txt", Old: "world", New: "cella"})
	if err != nil {
		t.Fatalf("agentEdit returned error: %v", err)
	}
	if edit.Replacements != 1 || string(fake.files["hello.txt"]) != "hello cella\n" {
		t.Fatalf("unexpected edit: %#v file=%q", edit, fake.files["hello.txt"])
	}

	_, bash, err := mt.agentBash(ctx, nil, mcpBashArgs{Sandbox: "frontend", Command: "echo done", Cwd: ".", TimeoutSeconds: 1})
	if err != nil {
		t.Fatalf("agentBash returned error: %v", err)
	}
	if bash.Output != "done\n" || bash.ExitCode == nil || *bash.ExitCode != 0 {
		t.Fatalf("unexpected bash result: %#v", bash)
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
	if upload.Dest != "inbox" || string(fake.files["inbox/uploaded.txt"]) != "payload\n" {
		t.Fatalf("unexpected upload result: %#v files=%#v", upload, fake.files)
	}

	_, download, err := mt.agentDownload(ctx, nil, mcpDownloadArgs{Sandbox: "frontend", Paths: []string{"hello.txt"}})
	if err != nil {
		t.Fatalf("agentDownload returned error: %v", err)
	}
	if download.Bytes == 0 || download.TarBase64 == "" {
		t.Fatalf("unexpected download result: %#v", download)
	}
}

func TestMCPManagementTools(t *testing.T) {
	fake := newFakeMCPAPI(t)
	fake.sandboxes["demo"] = sandboxDTO{ID: "demo", Name: "demo", State: "running", Tier: "ephemeral"}
	fake.files["artifact.txt"] = []byte("artifact\n")
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
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	registerAgentTools(srv, &mcpTools{})
	registerManagementTools(srv, &mcpTools{})
}

func TestMCPErrorPaths(t *testing.T) {
	fake := newFakeMCPAPI(t)
	fake.sandboxes["demo"] = sandboxDTO{ID: "demo", Name: "demo", State: "stopped", Tier: "ephemeral"}
	fake.files["hello.txt"] = []byte("hello world\n")
	fake.files["multi.txt"] = []byte("x x\n")
	fake.files["binary.bin"] = []byte{0xff, 0xfe}
	defer fake.server.Close()

	mt := &mcpTools{c: &api.Client{BaseURL: fake.server.URL, HTTP: fake.server.Client()}}
	ctx := context.Background()

	if _, err := mt.resolveSandbox(""); err == nil {
		t.Fatal("resolveSandbox accepted empty selector")
	}
	if _, err := mt.ensureSandbox(ctx, "demo"); err == nil {
		t.Fatal("ensureSandbox auto-started with autoStart disabled")
	}
	mt.autoStart = true

	if _, _, err := mt.agentRead(ctx, nil, mcpReadArgs{Sandbox: "demo", Path: "/etc/passwd"}); err == nil {
		t.Fatal("agentRead accepted absolute path")
	}
	if _, _, err := mt.agentRead(ctx, nil, mcpReadArgs{Sandbox: "demo", Path: "binary.bin"}); err == nil {
		t.Fatal("agentRead accepted binary file")
	}
	if _, _, _, err := mt.readTextFile(ctx, "demo", "hello.txt", -1, 1); err == nil {
		t.Fatal("readTextFile accepted negative offset")
	}
	if _, _, _, err := mt.readTextFile(ctx, "demo", "hello.txt", 0, 5<<20+1); err == nil {
		t.Fatal("readTextFile accepted overlarge limit")
	}
	if _, _, _, err := mt.readTextFile(ctx, "demo", "missing.txt", 0, 1); err == nil {
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
	startCount int
	nextID     int
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
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.nextID++
		id := "cmd-" + string(rune('0'+f.nextID))
		exit := 0
		cmd := strings.Join(req.Argv, " ")
		output := "done\n"
		if strings.Contains(cmd, "find ") {
			output = "hello.txt\nsrc/main.go\n"
		}
		if strings.Contains(cmd, "grep ") {
			output = "src/main.go:1:package main\n"
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
			Paths []string `json:"paths"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		files := map[string]string{}
		if len(req.Paths) == 0 {
			for name, body := range f.files {
				files[name] = string(body)
			}
		}
		for _, name := range req.Paths {
			if body, ok := f.files[name]; ok {
				files[name] = string(body)
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
		file, _, err := r.FormFile("tarball")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer file.Close()
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
		name := hdr.Name
		if dest != "" {
			name = path.Join(dest, name)
		}
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
