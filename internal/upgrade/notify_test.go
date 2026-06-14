package upgrade

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrintNotice(t *testing.T) {
	var b bytes.Buffer
	printNotice(&b, "0.2.30", "v0.3.0")
	out := b.String()
	if !strings.Contains(out, "v0.2.30 -> v0.3.0") {
		t.Errorf("notice missing version transition: %q", out)
	}
	if !strings.Contains(out, "latere upgrade") {
		t.Errorf("notice should tell the user to run latere upgrade: %q", out)
	}
}

// redirectServer serves the releases/latest redirect to the given tag.
func redirectServer(t *testing.T, tag string) func() {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/"+repoSlug+"/releases/tag/"+tag, http.StatusFound)
	}))
	old := githubBase
	githubBase = srv.URL
	return func() {
		githubBase = old
		srv.Close()
	}
}

func TestRunCheckOnlyReportsUpdate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	defer redirectServer(t, "v9.9.9")()

	var out bytes.Buffer
	if err := Run(context.Background(), "0.1.0", true, &out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "0.1.0 -> v9.9.9") {
		t.Errorf("check output should report the update: %q", out.String())
	}
	// The check should refresh the cache so a later notice matches.
	if got := loadState().LatestVersion; got != "v9.9.9" {
		t.Errorf("cached latest = %q, want v9.9.9", got)
	}
}

func TestRunAlreadyLatest(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	defer redirectServer(t, "v1.0.0")()

	var out bytes.Buffer
	if err := Run(context.Background(), "1.0.0", false, &out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "already the latest") {
		t.Errorf("expected already-latest message, got: %q", out.String())
	}
}
