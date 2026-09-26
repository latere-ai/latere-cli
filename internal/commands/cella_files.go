// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/ulikunitz/xz"

	cellaclient "latere.ai/x/cella/client"
	"latere.ai/x/pkg/relpath"
)

// ---- export / import / upload ----

func newCeExportCmd() *cobra.Command {
	var (
		apiURL string
		srcDir string
		out    string
	)
	cmd := &cobra.Command{
		Use:   "export <name|id> [paths...]",
		Short: "Stream a tar of files from the cella workspace.",
		Long: `Export files from a Cella workspace as a tar stream.

Relative paths are resolved under --src-dir, /workspace by default; with no
path the whole of --src-dir is exported. Entries in the archive are named
relative to the workspace. The tar is written to stdout unless --output
names a file, which is written only once the whole archive has arrived.`,
		Example: `  latere cella export dev -o workspace.tar
  latere cella export dev src package.json -o app.tar
  latere cella export dev --src-dir /workspace/results logs -o results.tar`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if out == "" {
				return errors.New("--output cannot be empty; use '-' for stdout")
			}
			root := resolveCellaPath(defaultStr(srcDir, cellaWorkspace))
			paths := []string{root}
			if len(args) > 1 {
				paths = paths[:0]
				for _, p := range args[1:] {
					if path.IsAbs(p) {
						paths = append(paths, path.Clean(p))
					} else {
						paths = append(paths, path.Join(root, p))
					}
				}
			}
			c, err := cellaClient(apiURL)
			if err != nil {
				return err
			}
			stream, err := c.ExportTar(cmd.Context(), args[0], paths)
			if err != nil {
				return err
			}
			defer func() { _ = stream.Close() }()
			body := completeTar{stream}
			if out != "-" {
				return saveDownload(out, body)
			}
			_, err = io.Copy(cmd.OutOrStdout(), body)
			return err
		},
	}
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", cellaURLUsage)
	f.StringVar(&srcDir, "src-dir", "", "directory inside the cella that relative paths start from; default /workspace")
	f.StringVarP(&out, "output", "o", "-", "output tar path (- for stdout)")
	return cmd
}

// completeTar reads an export and turns the failure the control plane can
// report only after the first byte, in the stream's trailer, into the read
// error at the end of the body. A download then fails rather than leaving a
// truncated archive that looks whole.
type completeTar struct{ *cellaclient.TarStream }

func (t completeTar) Read(p []byte) (int, error) {
	n, err := t.TarStream.Read(p)
	if errors.Is(err, io.EOF) {
		if failure := t.Err(); failure != nil {
			return n, failure
		}
	}
	return n, err
}

// importTo streams the tar that write produces into the sandbox below dest.
// The archive is produced while it is sent, so an input of any size holds
// nothing in memory, and a failure producing it ends the upload with that
// failure rather than with a truncated archive.
func importTo(ctx context.Context, c *cellaclient.Client, ref, dest string, write func(io.Writer) error) error {
	pr, pw := io.Pipe()
	produced := make(chan error, 1)
	go func() {
		err := write(pw)
		_ = pw.CloseWithError(err)
		produced <- err
	}()
	sendErr := c.ImportTar(ctx, ref, dest, pr)
	_ = pr.CloseWithError(errUploadEnded)
	// A producer that stopped because the upload ended, the transport having
	// closed the body on an early answer, reports nothing of its own: the
	// answer is the error. Any other failure of the producer is the cause.
	if err := <-produced; err != nil && !errors.Is(err, errUploadEnded) && !errors.Is(err, io.ErrClosedPipe) {
		return err
	}
	return sendErr
}

// errUploadEnded is what a producer's write returns once the upload it feeds
// has ended.
var errUploadEnded = errors.New("the upload ended")

func newCeImportCmd() *cobra.Command {
	var (
		apiURL  string
		dest    string
		input   string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "import <name|id>",
		Short: "Upload files into the cella workspace (reads stdin or --input).",
		Long: `Import files into a Cella workspace.

Tar archives are extracted. Gzip, bzip2, and XZ compression are decoded
before upload, including when reading tar from stdin. Zip archives are
converted to tar. A regular file is copied as a single file into the
destination directory.`,
		Example: `  latere cella import dev --input workspace.tar
  latere cella import dev --input app.zip --dest /workspace/app
  tar -cf - src package.json | latere cella import dev --dest /workspace/app`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if input == "" {
				return errors.New("--input cannot be empty; use '-' for stdin")
			}
			if timeout < 0 {
				return fmt.Errorf("--timeout must not be negative")
			}
			var (
				src          = cmd.InOrStdin()
				srcFile      *os.File
				formFilename = "stdin"
				inputKind    = importInputTar
			)
			if input != "-" {
				// Opening a FIFO can block before the timeout starts. Named
				// inputs also need seeking for format detection and ZIP conversion.
				info, err := os.Stat(input)
				if err != nil {
					return err
				}
				if !info.Mode().IsRegular() {
					return fmt.Errorf("import input %q is not a regular file; use stdin for tar streams", input)
				}
				f, err := os.Open(input)
				if err != nil {
					return err
				}
				defer func() { _ = f.Close() }()
				src = f
				srcFile = f
				formFilename = filepath.Base(input)
				inputKind, err = classifyImportInput(input, f)
				if err != nil {
					return err
				}
			}
			c, err := cellaClient(apiURL)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			target := resolveCellaPath(defaultStr(dest, cellaWorkspace))
			var payload importPayloadWriter
			err = importTo(ctx, c, args[0], target, func(w io.Writer) error {
				payload.Writer = w
				switch inputKind {
				case importInputRegularFile:
					return writeSingleFileTar(&payload, input, srcFile)
				case importInputZip:
					return writeZipAsTar(&payload, input, srcFile)
				default:
					return copyImportTar(&payload, src)
				}
			})
			if err != nil {
				return err
			}
			return printJSON(cmd.OutOrStdout(), map[string]any{"imported": formFilename, "bytes": payload.bytes, "dest": target})
		},
	}
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", cellaURLUsage)
	f.StringVar(&dest, "dest", "", "destination dir in the cella; default /workspace")
	f.StringVarP(&input, "input", "i", "-", "input path; tar archives are extracted, regular files are copied")
	f.DurationVar(&timeout, "timeout", 30*time.Minute, "time allowed for the upload and extraction (0 disables)")
	return cmd
}

// importPayloadWriter counts the tar bytes after conversion or decompression.
// Read bytes only after the upload has succeeded.
type importPayloadWriter struct {
	io.Writer
	bytes int64
}

func (w *importPayloadWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	w.bytes += int64(n)
	return n, err
}

type importInputKind int

const (
	importInputTar importInputKind = iota
	importInputRegularFile
	importInputZip
)

func classifyImportInput(name string, f *os.File) (importInputKind, error) {
	info, err := f.Stat()
	if err != nil {
		return importInputTar, err
	}
	if !info.Mode().IsRegular() {
		return importInputTar, fmt.Errorf("import input %q is not a regular file; use stdin for tar streams", name)
	}
	if hasZipExtension(name) {
		return importInputZip, nil
	}
	if hasTarExtension(name) {
		return importInputTar, nil
	}
	kind, err := sniffImportInput(f)
	if err != nil {
		return importInputTar, err
	}
	return kind, nil
}

func hasTarExtension(name string) bool {
	name = strings.ToLower(name)
	for _, suffix := range []string{".tar", ".tar.gz", ".tgz", ".tar.bz2", ".tbz", ".tbz2", ".tar.xz", ".txz"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func hasZipExtension(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), ".zip")
}

func sniffImportInput(f *os.File) (importInputKind, error) {
	var block [512]byte
	n, err := io.ReadFull(f, block[:4])
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return importInputTar, err
	}
	if _, seekErr := f.Seek(0, io.SeekStart); seekErr != nil {
		return importInputTar, seekErr
	}
	if n >= 4 {
		switch string(block[:4]) {
		case "PK\x03\x04", "PK\x05\x06", "PK\x07\x08":
			return importInputZip, nil
		}
	}
	// Probe one decoded tar header. Compressed non-archives remain regular
	// files; upload reads the complete archive again to verify its checksum.
	decoded, decodeErr := newImportTarReader(f)
	if decodeErr == nil {
		n, decodeErr = io.ReadFull(decoded, block[:])
		_ = decoded.Close()
	}
	if _, seekErr := f.Seek(0, io.SeekStart); seekErr != nil {
		return importInputTar, seekErr
	}
	if decodeErr == nil && n == len(block) && block != ([512]byte{}) {
		header, headerErr := tar.NewReader(bytes.NewReader(block[:])).Next()
		// A complete nonzero block must pass header parsing before Next
		// can request following PAX/GNU metadata and hit our probe's EOF.
		if header != nil || errors.Is(headerErr, io.EOF) || errors.Is(headerErr, io.ErrUnexpectedEOF) {
			return importInputTar, nil
		}
	}
	return importInputRegularFile, nil
}

// copyImportTar sends plain tar to the API, detecting compression from the
// stream so named archives and stdin behave identically. Copy through EOF to
// surface decompression and checksum errors before completing the multipart body.
func copyImportTar(dst io.Writer, src io.Reader) error {
	decoded, err := newImportTarReader(src)
	if err != nil {
		return err
	}
	defer func() { _ = decoded.Close() }()
	_, err = io.Copy(dst, decoded)
	return err
}

func newImportTarReader(src io.Reader) (io.ReadCloser, error) {
	buffered := bufio.NewReader(src)
	header, err := buffered.Peek(6)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	switch {
	case bytes.HasPrefix(header, []byte{0x1f, 0x8b}):
		return gzip.NewReader(buffered)
	case bytes.HasPrefix(header, []byte("BZh")):
		return io.NopCloser(bzip2.NewReader(buffered)), nil
	case bytes.HasPrefix(header, []byte{0xfd, '7', 'z', 'X', 'Z', 0}):
		reader, err := xz.NewReader(buffered)
		if err != nil {
			return nil, err
		}
		return io.NopCloser(reader), nil
	default:
		return io.NopCloser(buffered), nil
	}
}

func writeSingleFileTar(dst io.Writer, name string, f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	tw := tar.NewWriter(dst)
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	hdr.Name = filepath.Base(name)
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := io.Copy(tw, f); err != nil {
		return err
	}
	return tw.Close()
}

func writeZipAsTar(dst io.Writer, name string, f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(f, info.Size())
	if err != nil {
		return fmt.Errorf("read zip %s: %w", name, err)
	}
	tw := tar.NewWriter(dst)
	for _, zf := range zr.File {
		if !safeArchivePath(zf.Name) {
			return fmt.Errorf("zip entry has unsafe path: %s", zf.Name)
		}
		hdr, err := tar.FileInfoHeader(zf.FileInfo(), "")
		if err != nil {
			return err
		}
		hdr.Name = strings.TrimPrefix(zf.Name, "./")
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if zf.FileInfo().IsDir() {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, rc)
		closeErr := rc.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return tw.Close()
}

// safeArchivePath accepts file and directory names below the archive root.
// One leading "./" and a trailing directory slash are allowed; the remaining
// path must be clean, relative, nonempty, and free of NUL or ".." elements.
func safeArchivePath(name string) bool {
	name = strings.TrimPrefix(name, "./")
	name = strings.TrimSuffix(name, "/")
	clean, err := relpath.Clean(name)
	return err == nil && clean == name && clean != "."
}

// ---- granular file ops ----

// newCeCatCmd streams a single file to the command's configured output.
func newCeCatCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:     "cat <name|id> <path>",
		Short:   "Stream a file from the cella to stdout.",
		Example: `  latere cella cat dev /workspace/out.log`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := cellaClient(apiURL)
			if err != nil {
				return err
			}
			body, err := c.FileGet(cmd.Context(), args[0], resolveCellaPath(args[1]))
			if err != nil {
				return err
			}
			defer func() { _ = body.Close() }()
			_, err = io.Copy(cmd.OutOrStdout(), body)
			return err
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", cellaURLUsage)
	return cmd
}

// newCeWriteCmd writes a single file from --input or the configured input
// stream. The body streams, and the control plane stages it, so a write that
// ends early leaves the previous file whole.
func newCeWriteCmd() *cobra.Command {
	var (
		apiURL string
		input  string
	)
	cmd := &cobra.Command{
		Use:   "write <name|id> <path>",
		Short: "Write a file into the cella (reads stdin or --input).",
		Long: `Write one file into the cella from stdin or --input, creating any
missing parent directories.`,
		Example: `  echo hi | latere cella write dev /workspace/note.txt
  latere cella write dev /workspace/app.tar -f app.tar`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			src := cmd.InOrStdin()
			if input != "" && input != "-" {
				f, err := os.Open(input)
				if err != nil {
					return err
				}
				defer func() { _ = f.Close() }()
				src = f
			}
			c, err := cellaClient(apiURL)
			if err != nil {
				return err
			}
			return c.FilePut(cmd.Context(), args[0], resolveCellaPath(args[1]), "", src)
		},
	}
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", cellaURLUsage)
	f.StringVarP(&input, "input", "f", "", "read content from this file (- or empty for stdin)")
	return cmd
}

// newCeLsCmd lists a directory inside the cella.
func newCeLsCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:     "ls <name|id> <path>",
		Short:   "List a directory inside the cella.",
		Long:    "List a directory's entries, one per line: the mode in octal, the size in bytes, and the name, with a trailing slash on a directory.",
		Example: `  latere cella ls dev /workspace`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := cellaClient(apiURL)
			if err != nil {
				return err
			}
			entries, _, err := c.FileList(cmd.Context(), args[0], resolveCellaPath(args[1]))
			if err != nil {
				return err
			}
			for _, e := range entries {
				name := e.Name
				if e.IsDir {
					name += "/"
				}
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%d\t%s\n", e.Mode, e.Size, name); err != nil {
					return fmt.Errorf("write directory listing: %w", err)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", cellaURLUsage)
	return cmd
}

type cellaUploadFile struct{ rel, local string }

// collectCellaUploadFiles validates every source before the request starts.
// Special files can block on open/read or silently upload an empty body.
func collectCellaUploadFiles(sources []string) ([]cellaUploadFile, error) {
	var files []cellaUploadFile
	addFile := func(local, rel string) error {
		info, err := os.Stat(local)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("upload source %q is not a regular file", local)
		}
		files = append(files, cellaUploadFile{rel: filepath.ToSlash(rel), local: local})
		return nil
	}
	for _, src := range sources {
		info, err := os.Stat(src)
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			entry, err := os.Lstat(src)
			if err != nil {
				return nil, err
			}
			if entry.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("upload source %q is not a regular file", src)
			}
			// Resolve symlinks before cleaning ..: WalkDir joins child paths
			// lexically, which can otherwise select a different local directory.
			root, err := filepath.EvalSymlinks(src)
			if err != nil {
				return nil, err
			}
			contentsOnly := root == "."
			root, err = filepath.Abs(root)
			if err != nil {
				return nil, err
			}
			prefix := filepath.Base(root)
			if contentsOnly || filepath.Dir(root) == root {
				prefix = ""
			}
			if err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				rel, err := filepath.Rel(root, p)
				if err != nil {
					return err
				}
				return addFile(p, filepath.Join(prefix, rel))
			}); err != nil {
				return nil, err
			}
			continue
		}
		if err := addFile(src, filepath.Base(src)); err != nil {
			return nil, err
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no files to upload")
	}
	return files, nil
}

// newCeUploadCmd streams files and folders into the cella as one tar,
// preserving folder structure: each file's entry is its path relative to the
// destination.
func newCeUploadCmd() *cobra.Command {
	var (
		apiURL  string
		dest    string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "upload <name|id> <src...> --dest D",
		Short: "Stream files/folders into the cella (folder-preserving).",
		Example: `  latere cella upload dev ./dist --dest /workspace
  latere cella upload dev a.txt b.txt --dest /workspace/tmp`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if timeout < 0 {
				return fmt.Errorf("--timeout must not be negative")
			}
			files, err := collectCellaUploadFiles(args[1:])
			if err != nil {
				return err
			}
			c, err := cellaClient(apiURL)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			target := resolveCellaPath(defaultStr(dest, cellaWorkspace))
			var uploaded int64
			if err := importTo(ctx, c, args[0], target, func(w io.Writer) error {
				n, err := writeUploadTar(w, files)
				uploaded = n
				return err
			}); err != nil {
				return err
			}
			fprintf(cmd.OutOrStdout(), "uploaded %d files (%d bytes) to %s\n", len(files), uploaded, target)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "", cellaURLUsage)
	f.StringVar(&dest, "dest", "", "destination directory inside the cella; default /workspace")
	f.DurationVar(&timeout, "timeout", 5*time.Minute, "upload timeout (0 disables)")
	return cmd
}

// writeUploadTar writes the collected files as one tar and returns the bytes
// of file content it wrote. A file that changed size while it was read fails
// the archive rather than sending a header that disagrees with its body.
func writeUploadTar(w io.Writer, files []cellaUploadFile) (int64, error) {
	tw := tar.NewWriter(w)
	var total int64
	for _, uf := range files {
		f, err := os.Open(uf.local)
		if err != nil {
			return total, err
		}
		info, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return total, err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			_ = f.Close()
			return total, err
		}
		hdr.Name = uf.rel
		if err := tw.WriteHeader(hdr); err != nil {
			_ = f.Close()
			return total, err
		}
		n, err := io.Copy(tw, f)
		total += n
		closeErr := f.Close()
		if err != nil {
			return total, err
		}
		if closeErr != nil {
			return total, closeErr
		}
	}
	return total, tw.Close()
}

// newCeMkdirCmd creates a directory inside the cella.
func newCeMkdirCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:     "mkdir <name|id> <path>",
		Short:   "Create a directory, and its missing parents, inside the cella.",
		Example: `  latere cella mkdir dev /workspace/build`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := cellaClient(apiURL)
			if err != nil {
				return err
			}
			return c.FileMkdir(cmd.Context(), args[0], resolveCellaPath(args[1]))
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", cellaURLUsage)
	return cmd
}

// newCeRmCmd deletes a file or directory tree inside the cella.
func newCeRmCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:     "rm <name|id> <path>",
		Short:   "Delete a file or directory (recursive) inside the cella.",
		Example: `  latere cella rm dev /workspace/old`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := cellaClient(apiURL)
			if err != nil {
				return err
			}
			return c.FileRemove(cmd.Context(), args[0], resolveCellaPath(args[1]))
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", cellaURLUsage)
	return cmd
}

// newCeMvCmd renames or moves a file or directory inside the cella.
func newCeMvCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:     "mv <name|id> <from> <to>",
		Short:   "Rename or move a file or directory inside the cella.",
		Example: `  latere cella mv dev /workspace/a.txt /workspace/b.txt`,
		Args:    cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := cellaClient(apiURL)
			if err != nil {
				return err
			}
			return c.FileMove(cmd.Context(), args[0], resolveCellaPath(args[1]), resolveCellaPath(args[2]))
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", cellaURLUsage)
	return cmd
}
