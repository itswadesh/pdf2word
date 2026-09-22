package ocr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// DefaultLang is the Tesseract language used when none is configured.
const DefaultLang = "eng"

// ErrNotFound is returned by Find when no Tesseract executable can be located.
var ErrNotFound = errors.New("tesseract executable not found")

// Tesseract runs the tesseract command-line program. It is safe for
// concurrent use; each Recognize call is an independent process.
type Tesseract struct {
	Path    string // executable path; empty means "tesseract" on PATH
	Lang    string // language(s), e.g. "eng" or "eng+deu"; empty means DefaultLang
	Threads int    // OpenMP threads per process (OMP_THREAD_LIMIT); 0 leaves the default
}

// Name implements Engine.
func (t *Tesseract) Name() string { return "tesseract" }

func (t *Tesseract) path() string {
	if t.Path == "" {
		return "tesseract"
	}
	return t.Path
}

func (t *Tesseract) lang() string {
	if t.Lang == "" {
		return DefaultLang
	}
	return t.Lang
}

// args builds the command line for recognising one image file to stdout.
func (t *Tesseract) args(file string) []string {
	return []string{file, "stdout", "-l", t.lang()}
}

// Available checks that the executable runs by invoking `--version`.
func (t *Tesseract) Available(ctx context.Context) error {
	_, err := t.Version(ctx)
	return err
}

// Version runs `--version` and returns its first line, e.g.
// "tesseract v5.4.0.20240606".
func (t *Tesseract) Version(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, t.path(), "--version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s --version: %w: %s", t.path(), err, firstLine(out))
	}
	return firstLine(out), nil
}

// Recognize implements Engine by writing img to a temporary file and running
// tesseract on it.
func (t *Tesseract) Recognize(ctx context.Context, img []byte, ext string) (string, error) {
	dir, err := os.MkdirTemp("", "pdf2word-ocr-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)

	ext = strings.TrimPrefix(strings.ToLower(ext), ".")
	if ext == "" {
		ext = "png"
	}
	file := filepath.Join(dir, "page."+ext)
	if err := os.WriteFile(file, img, 0o600); err != nil {
		return "", err
	}

	cmd := exec.CommandContext(ctx, t.path(), t.args(file)...)
	if t.Threads > 0 {
		cmd.Env = append(os.Environ(), fmt.Sprintf("OMP_THREAD_LIMIT=%d", t.Threads))
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %w: %s", t.path(), err, strings.TrimSpace(stderr.String()))
	}
	return strings.ReplaceAll(stdout.String(), "\r\n", "\n"), nil
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s) // drops a trailing \r from Windows output
}

// knownDirs lists installation directories checked after PATH. It is a
// variable so tests can override it.
var knownDirs = defaultKnownDirs()

func defaultKnownDirs() []string {
	if runtime.GOOS == "windows" {
		dirs := []string{
			`C:\Program Files\Tesseract-OCR`,
			`C:\Program Files (x86)\Tesseract-OCR`,
			`C:\tools\Tesseract-OCR`,
		}
		if la := os.Getenv("LOCALAPPDATA"); la != "" {
			dirs = append([]string{filepath.Join(la, "Programs", "Tesseract-OCR")}, dirs...)
		}
		return dirs
	}
	return []string{"/usr/local/bin", "/opt/homebrew/bin", "/usr/bin", "/opt/local/bin"}
}

func exeName() string {
	if runtime.GOOS == "windows" {
		return "tesseract.exe"
	}
	return "tesseract"
}

// Find locates the Tesseract executable. Resolution order:
//
//  1. explicit (a path, or a bare command name looked up on PATH)
//  2. the TESSERACT_CMD environment variable
//  3. "tesseract" on PATH
//  4. well-known installation directories
//
// It returns ErrNotFound (possibly wrapped) when nothing is found.
func Find(explicit string) (string, error) {
	if explicit != "" {
		return resolve(explicit)
	}
	if env := os.Getenv("TESSERACT_CMD"); env != "" {
		return resolve(env)
	}
	if p, err := exec.LookPath("tesseract"); err == nil {
		return p, nil
	}
	for _, d := range knownDirs {
		p := filepath.Join(d, exeName())
		if isFile(p) {
			return p, nil
		}
	}
	return "", ErrNotFound
}

func resolve(p string) (string, error) {
	if filepath.IsAbs(p) || strings.ContainsAny(p, `/\`) {
		if isFile(p) {
			return p, nil
		}
		return "", fmt.Errorf("%w at %q", ErrNotFound, p)
	}
	found, err := exec.LookPath(p)
	if err != nil {
		return "", fmt.Errorf("%w: %q is not on PATH", ErrNotFound, p)
	}
	return found, nil
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
