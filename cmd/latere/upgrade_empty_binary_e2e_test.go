// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/latere-ai/latere-cli/internal/commands"
)

func TestUpgradeRejectsEmptyBinaryE2E(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("self-replacement subprocess test")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	disposable := filepath.Join(dir, "disposable-latere")
	if err := os.WriteFile(disposable, original, 0755); err != nil {
		t.Fatal(err)
	}
	originalHash := sha256.Sum256(original)
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "latere", Typeflag: tar.TypeReg, Mode: 0755, Size: 0}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	checksum := sha256.Sum256(archive.Bytes())
	asset := fmt.Sprintf("latere_9.9.9_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		if r.Method != http.MethodGet {
			t.Errorf("method=%s", r.Method)
		}
		switch r.URL.Path {
		case "/latere-ai/latere-cli/releases/download/v9.9.9/" + asset:
			_, _ = w.Write(archive.Bytes())
		case "/latere-ai/latere-cli/releases/download/v9.9.9/checksums.txt":
			_, _ = fmt.Fprintf(w, "%x  %s\n", checksum, asset)
		default:
			t.Errorf("unexpected release path=%s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, disposable, "-test.run=^TestUpgradeEmptyBinaryHelperProcess$", "--", "upgrade", "v9.9.9")
	command.Env = append(os.Environ(), "LATERE_TEST_UPGRADE_SERVER="+server.URL, "LATERE_TEST_UPGRADE_COPY="+disposable, "LATERE_NO_UPDATE_CHECK=1", "OTEL_SDK_DISABLED=true", "XDG_CONFIG_HOME="+dir, "LATERE_TOKEN_FILE="+filepath.Join(dir, "absent-token.json"), "LATERE_AUTH_TOKEN_FILE="+filepath.Join(dir, "absent-auth.json"))
	output, runErr := command.CombinedOutput()
	if exit, ok := errors.AsType[*exec.ExitError](runErr); !ok || exit.ExitCode() != 1 || !strings.Contains(string(output), "empty latere binary") || strings.Contains(string(output), "Now on") {
		t.Errorf("upgrade error=%v output=%q", runErr, output)
	}
	installed, err := os.ReadFile(disposable)
	if err != nil || sha256.Sum256(installed) != originalHash {
		t.Errorf("executable changed: size=%d error=%v", len(installed), err)
	}
	if downloads.Load() != 2 {
		t.Errorf("downloads=%d, want archive and checksum", downloads.Load())
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, ".latere-upgrade-*"))
	if err != nil || len(leftovers) != 0 {
		t.Errorf("upgrade leftovers=%v error=%v", leftovers, err)
	}
}

type upgradeFixtureTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (tr upgradeFixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != "github.com" {
		return nil, fmt.Errorf("unexpected upgrade host %q", req.URL.Host)
	}
	clone := req.Clone(req.Context())
	clone.URL.Scheme, clone.URL.Host = tr.target.Scheme, tr.target.Host
	clone.Host = tr.target.Host
	return tr.base.RoundTrip(clone)
}

func TestUpgradeEmptyBinaryHelperProcess(t *testing.T) {
	fixture := os.Getenv("LATERE_TEST_UPGRADE_SERVER")
	if fixture == "" {
		return
	}
	// Refuse to exercise self-replacement unless running the disposable copy.
	executable, err := os.Executable()
	if err != nil || executable != os.Getenv("LATERE_TEST_UPGRADE_COPY") || filepath.Base(executable) != "disposable-latere" {
		t.Fatal("not running the disposable executable")
	}
	target, err := url.Parse(fixture)
	if err != nil || target.Hostname() != "127.0.0.1" {
		t.Fatal("expected loopback fixture")
	}
	http.DefaultTransport = upgradeFixtureTransport{target: target, base: &http.Transport{Proxy: nil}}
	root := commands.NewRoot("v1.0.0")
	root.SetArgs(os.Args[3:])
	root.SetOut(os.Stdout)
	root.SetErr(io.Discard)
	if err := root.Execute(); err != nil {
		os.Exit(commands.HandleExitError(os.Stderr, err))
	}
	os.Exit(0)
}
