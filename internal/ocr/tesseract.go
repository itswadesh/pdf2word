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
	"strconv"
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
	return t.run(ctx, img, ext)
}

// Word is one recognised word with its box in image pixels (origin top-left).
type Word struct {
	Text       string
	Left, Top  int
	Width      int
	Height     int
	LineTop    int // box of the text line the word belongs to
	LineHeight int
	Block      int // Tesseract block, paragraph and line ids (for grouping)
	Par        int
	Line       int
	Conf       float64
}

// WordEngine is implemented by engines that can report word positions.
type WordEngine interface {
	Engine
	RecognizeWords(ctx context.Context, img []byte, ext string) ([]Word, error)
}

// RecognizeWords runs tesseract in TSV mode and returns the words with their
// boxes. It implements WordEngine.
func (t *Tesseract) RecognizeWords(ctx context.Context, img []byte, ext string) ([]Word, error) {
	out, err := t.run(ctx, img, ext, "tsv")
	if err != nil {
		return nil, err
	}
	return parseTSV(out), nil
}

// run executes tesseract on img with the given output config (e.g. "tsv"
// or none for plain text) and returns stdout.
func (t *Tesseract) run(ctx context.Context, img []byte, ext string, configs ...string) (string, error) {
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
	args := append(t.args(file), configs...)
	cmd := exec.CommandContext(ctx, t.path(), args...)
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

// parseTSV reads Tesseract's TSV output (level page block par line word
// left top width height conf text). Level 4 rows carry line boxes, level 5
// rows words.
func parseTSV(tsv string) []Word {
	type lineKey struct{ block, par, line int }
	type lineBox struct{ top, height int }
	lineBoxes := map[lineKey]lineBox{}
	var words []Word
	for i, row := range strings.Split(tsv, "\n") {
		f := strings.Split(row, "\t")
		if i == 0 || len(f) < 12 {
			continue
		}
		level := atoi(f[0])
		key := lineKey{atoi(f[2]), atoi(f[3]), atoi(f[4])}
		switch level {
		case 4:
			lineBoxes[key] = lineBox{top: atoi(f[7]), height: atoi(f[9])}
		case 5:
			text := strings.TrimSpace(f[11])
			if text == "" {
				continue
			}
			words = append(words, Word{
				Text: text, Left: atoi(f[6]), Top: atoi(f[7]), Width: atoi(f[8]), Height: atoi(f[9]),
				Block: key.block, Par: key.par, Line: key.line, Conf: atof(f[10]),
			})
		}
	}
	for i := range words {
		w := &words[i]
		if lb, ok := lineBoxes[lineKey{w.Block, w.Par, w.Line}]; ok && lb.height > 0 {
			w.LineTop, w.LineHeight = lb.top, lb.height
		} else {
			w.LineTop, w.LineHeight = w.Top, w.Height
		}
	}
	return words
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func atof(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
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

// Bundled, when set, returns the path of a Tesseract runtime shipped inside
// the program (see internal/tessbundle). It is consulted after explicit
// settings and before PATH, so a bundled copy is the default but the user can
// still point at another installation.
var Bundled func() (string, error)

// Find locates the Tesseract executable. Resolution order:
//
//  1. explicit (a path, or a bare command name looked up on PATH)
//  2. the TESSERACT_CMD environment variable
//  3. the bundled runtime, if this build has one
//  4. "tesseract" on PATH
//  5. well-known installation directories
//
// It returns ErrNotFound (possibly wrapped) when nothing is found.
func Find(explicit string) (string, error) {
	if explicit != "" {
		return resolve(explicit)
	}
	if env := os.Getenv("TESSERACT_CMD"); env != "" {
		return resolve(env)
	}
	if Bundled != nil {
		if p, err := Bundled(); err == nil && isFile(p) {
			return p, nil
		}
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
