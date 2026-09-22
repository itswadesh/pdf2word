package pdflayout

import (
	"regexp"
	"strings"
	"unicode"
)

// fontInfo is what the document model needs to know about a PDF font.
type fontInfo struct {
	Family string
	Bold   bool
	Italic bool
}

var subsetPrefix = regexp.MustCompile(`^[A-Z]{6}\+`)

// PDF font descriptor flag bits (PDF 32000-1:2008 §9.8.2).
const (
	flagItalic     = 1 << 6
	flagForceBold  = 1 << 18
	weightBoldFrom = 600
)

// parseFont derives family and style from a PDF base font name plus the
// weight and flags PDFium reports. Standard names map to their Windows
// equivalents; other families pass through with camel case split into words.
func parseFont(name string, weight int, flags int) fontInfo {
	n := subsetPrefix.ReplaceAllString(strings.TrimSpace(name), "")
	fi := fontInfo{}

	lower := strings.ToLower(n)
	styleStart := len(n)
	if i := strings.IndexAny(n, "-,"); i >= 0 {
		styleStart = i
	}
	style := strings.ToLower(n[styleStart:])
	base := n[:styleStart]

	// Style words may also sit inside the base part ("ArialBold", "Arial Bold").
	fi.Bold = containsAny(style, "bold", "black", "heavy", "semibold", "demibold", "extrabold", "ultrabold") ||
		containsAny(strings.ToLower(base), "bold", "black", "heavy") ||
		weight >= weightBoldFrom || flags&flagForceBold != 0
	fi.Italic = containsAny(style, "italic", "oblique") ||
		containsAny(strings.ToLower(base), "italic", "oblique") ||
		flags&flagItalic != 0

	switch {
	case strings.Contains(lower, "times"):
		fi.Family = "Times New Roman"
	case strings.Contains(lower, "helvetica"), strings.Contains(lower, "arial"):
		fi.Family = "Arial"
	case strings.Contains(lower, "courier"):
		fi.Family = "Courier New"
	case strings.Contains(lower, "symbol"):
		fi.Family = "Symbol"
	case strings.Contains(lower, "zapf"), strings.Contains(lower, "dingbat"):
		fi.Family = "Wingdings"
	default:
		fi.Family = familyFromName(base)
	}
	return fi
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// familyFromName turns "BalooBhaina2Regular" or "Calibri" into a readable
// family name, dropping common foundry suffixes.
func familyFromName(base string) string {
	base = strings.TrimSpace(base)
	for _, suf := range []string{"PSMT", "PS", "MT", "Regular", "Roman", "Book", "Medium", "Light", "Bold", "Italic", "Oblique"} {
		base = strings.TrimSuffix(base, suf)
	}
	if base == "" {
		return ""
	}
	// Split camel case and letter/digit boundaries into words.
	var sb strings.Builder
	runes := []rune(base)
	for i, r := range runes {
		if i > 0 {
			prev := runes[i-1]
			switch {
			case unicode.IsUpper(r) && unicode.IsLower(prev),
				unicode.IsDigit(r) && unicode.IsLetter(prev),
				unicode.IsLetter(r) && unicode.IsDigit(prev):
				sb.WriteByte(' ')
			}
		}
		sb.WriteRune(r)
	}
	return strings.TrimSpace(strings.ReplaceAll(sb.String(), "  ", " "))
}
