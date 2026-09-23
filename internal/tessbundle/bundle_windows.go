//go:build windows

// Package tessbundle ships a Tesseract OCR runtime inside the executable
// (Windows x64 only) and unpacks it on first use, so the program works on a
// machine where Tesseract was never installed.
package tessbundle

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Version identifies the bundled Tesseract build. Bump it whenever the files
// under win64/ change so that existing installs are unpacked afresh.
// "-2": Odia (ori) language data added.
const Version = "5.4.0.20240606-2"

//go:embed win64
var files embed.FS

const stampName = ".pdf2word-bundle"

// Available reports whether this build carries a Tesseract runtime.
func Available() bool { return true }

// Path unpacks the bundled runtime if needed and returns the tesseract.exe
// path. The files live in a per-user cache directory keyed by Version, so
// unpacking happens once per machine and user.
func Path() (string, error) {
	dir := filepath.Join(cacheRoot(), "pdf2word", "tesseract-"+Version)
	exe := filepath.Join(dir, "tesseract.exe")
	if fileExists(filepath.Join(dir, stampName)) && fileExists(exe) {
		return exe, nil
	}
	if err := unpack(dir); err != nil {
		return "", fmt.Errorf("unpack bundled tesseract: %w", err)
	}
	return exe, nil
}

// Dir returns the directory the runtime is (or would be) unpacked to.
func Dir() string {
	return filepath.Join(cacheRoot(), "pdf2word", "tesseract-"+Version)
}

func cacheRoot() string {
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		return la
	}
	if d, err := os.UserCacheDir(); err == nil && d != "" {
		return d
	}
	return os.TempDir()
}

// unpack writes the embedded tree into a temporary sibling directory and
// renames it into place, so a half-written install is never picked up and
// two processes starting at once do not corrupt each other.
func unpack(dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	var rb [6]byte
	rand.Read(rb[:])
	tmp := dir + ".tmp-" + hex.EncodeToString(rb[:])
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	cleanup := func() { os.RemoveAll(tmp) }

	err := fs.WalkDir(files, "win64", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("win64", filepath.FromSlash(p))
		dst := filepath.Join(tmp, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := files.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o755)
	})
	if err != nil {
		cleanup()
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, stampName), []byte(Version+"\n"), 0o644); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmp, dir); err != nil {
		// Another process may have won the race; accept its copy.
		if fileExists(filepath.Join(dir, stampName)) {
			cleanup()
			return nil
		}
		// Or a stale, incomplete directory is in the way: replace it.
		os.RemoveAll(dir)
		if err2 := os.Rename(tmp, dir); err2 != nil {
			cleanup()
			return err2
		}
	}
	return nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
