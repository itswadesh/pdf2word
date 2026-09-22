//go:build !windows

// Package tessbundle ships a Tesseract OCR runtime inside the executable on
// Windows. On other platforms nothing is bundled and Tesseract is expected on
// PATH.
package tessbundle

import "errors"

// Version is empty when no runtime is bundled.
const Version = ""

// ErrUnavailable is returned by Path on platforms without a bundled runtime.
var ErrUnavailable = errors.New("no bundled tesseract for this platform")

// Available reports whether this build carries a Tesseract runtime.
func Available() bool { return false }

// Path always fails on this platform.
func Path() (string, error) { return "", ErrUnavailable }

// Dir returns "" on this platform.
func Dir() string { return "" }
