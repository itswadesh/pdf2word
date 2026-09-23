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
	// Fallback is the standard font to substitute when Family is not
	// installed on the machine that opens the Word file. Its metrics decide
	// whether kept lines still fit, so it is chosen by class: Times New
	// Roman for serif and unknown faces, Arial for sans, Courier New for
	// monospaced.
	Fallback string
}

var subsetPrefix = regexp.MustCompile(`^[A-Z]{6}\+`)

// PDF font descriptor flag bits (PDF 32000-1:2008 §9.8.2).
const (
	flagFixedPitch = 1 << 0
	flagItalic     = 1 << 6
	flagForceBold  = 1 << 18
	weightBoldFrom = 600
)

// Name fragments that mark sans-serif and monospaced families. Everything
// else (book faces such as Fournier, Minion or Garamond, and names that
// say nothing) falls back to Times New Roman, the most economical
// substitute, so kept lines do not wrap.
var (
	sansHints = []string{"helvetica", "arial", "calibri", "verdana", "tahoma", "segoe", "roboto", "sans", "futura", "gill",
		"frutiger", "myriad", "univers", "lato", "opensans", "open sans", "montserrat", "avenir", "franklin", "optima",
		"trebuchet", "gothic", "grotesk", "grotesque", "ubuntu", "raleway", "poppins", "inter", "nunito", "dejavu", "liberationsans"}
	monoHints = []string{"courier", "mono", "consolas", "menlo", "inconsolata", "code", "typewriter"}
)

// fallbackFor classifies a font by its name and descriptor flags.
func fallbackFor(lowerName string, flags int) string {
	switch {
	case flags&flagFixedPitch != 0, containsAny(lowerName, monoHints...):
		return "Courier New"
	case containsAny(lowerName, "serif") && !containsAny(lowerName, "sans"):
		return "Times New Roman"
	case containsAny(lowerName, sansHints...):
		return "Arial"
	default:
		return "Times New Roman"
	}
}

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
	switch fi.Family {
	case "Times New Roman", "Arial", "Courier New", "Symbol", "Wingdings", "":
		fi.Fallback = fi.Family
	default:
		fi.Fallback = fallbackFor(lower, flags)
		// A typeface the reader's machine will not have is replaced by its
		// standard stand-in outright: Word and LibreOffice would otherwise
		// each pick their own substitute, often a wider one, and lines kept
		// from the PDF would wrap.
		if !installedFont(fi.Family) {
			fi.Family = fi.Fallback
		}
	}
	return fi
}

// installedFont reports whether a family ships with Windows or Office, so
// a Word file may name it and expect it to be there.
func installedFont(family string) bool {
	key := strings.ToLower(strings.ReplaceAll(family, " ", ""))
	if _, ok := installedFonts[key]; ok {
		return true
	}
	// Family variants such as "Segoe UI Semibold" or "Arial Narrow".
	for _, prefix := range []string{"segoeui", "arial", "calibri", "cambria", "franklingothic", "lucida", "yugothic", "bodonimt", "gillsansmt", "bookman", "century", "sitka", "aptos", "bahnschrift", "eras", "berlinsansfb", "bernardmt", "copperplategothic", "tw"} {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

var installedFonts = func() map[string]struct{} {
	names := []string{
		// Windows 10/11
		"Arial", "Arial Black", "Arial Narrow", "Bahnschrift", "Calibri", "Calibri Light", "Cambria", "Cambria Math", "Candara",
		"Comic Sans MS", "Consolas", "Constantia", "Corbel", "Courier New", "Ebrima", "Franklin Gothic Medium", "Gabriola", "Gadugi",
		"Georgia", "Impact", "Ink Free", "Javanese Text", "Leelawadee UI", "Lucida Console", "Lucida Sans Unicode", "Malgun Gothic",
		"Microsoft Sans Serif", "MS Gothic", "MS UI Gothic", "MV Boli", "Nirmala UI", "Palatino Linotype", "Segoe Print", "Segoe Script",
		"Segoe UI", "SimSun", "Sitka", "Sylfaen", "Symbol", "Tahoma", "Times New Roman", "Trebuchet MS", "Verdana", "Webdings", "Wingdings",
		"Yu Gothic", "Meiryo", "Dubai", "Mongolian Baiti", "Myanmar Text", "Nirmala Text",
		// Office
		"Aptos", "Aptos Display", "Agency FB", "Algerian", "Baskerville Old Face", "Bell MT", "Berlin Sans FB", "Bernard MT Condensed",
		"Blackadder ITC", "Bodoni MT", "Book Antiqua", "Bookman Old Style", "Bookshelf Symbol 7", "Bradley Hand ITC", "Britannic Bold",
		"Broadway", "Brush Script MT", "Californian FB", "Calisto MT", "Castellar", "Centaur", "Century", "Century Gothic",
		"Century Schoolbook", "Chiller", "Colonna MT", "Cooper Black", "Copperplate Gothic Bold", "Copperplate Gothic Light", "Curlz MT",
		"Edwardian Script ITC", "Elephant", "Engravers MT", "Eras ITC", "Felix Titling", "Footlight MT Light", "Forte", "Franklin Gothic",
		"Freestyle Script", "French Script MT", "Garamond", "Gigi", "Gill Sans MT", "Gloucester MT Extra Condensed", "Goudy Old Style",
		"Goudy Stout", "Haettenschweiler", "Harlow Solid Italic", "Harrington", "High Tower Text", "Imprint MT Shadow", "Informal Roman",
		"Jokerman", "Juice ITC", "Kristen ITC", "Kunstler Script", "Lucida Bright", "Lucida Calligraphy", "Lucida Fax", "Lucida Handwriting",
		"Lucida Sans", "Lucida Sans Typewriter", "Magneto", "Maiandra GD", "Matura MT Script Capitals", "Mistral", "Modern No. 20",
		"Monotype Corsiva", "MS Outlook", "MS Reference Sans Serif", "MS Reference Specialty", "Niagara Engraved", "Niagara Solid",
		"OCR A Extended", "Old English Text MT", "Onyx", "Palace Script MT", "Papyrus", "Parchment", "Perpetua", "Perpetua Titling MT",
		"Playbill", "Poor Richard", "Pristina", "Rage Italic", "Ravie", "Rockwell", "Rockwell Condensed", "Rockwell Extra Bold",
		"Script MT Bold", "Showcard Gothic", "Snap ITC", "Stencil", "Tempus Sans ITC", "Tw Cen MT", "Tw Cen MT Condensed", "Viner Hand ITC",
		"Vivaldi", "Vladimir Script", "Wide Latin", "Wingdings 2", "Wingdings 3",
	}
	m := make(map[string]struct{}, len(names))
	for _, n := range names {
		m[strings.ToLower(strings.ReplaceAll(n, " ", ""))] = struct{}{}
	}
	return m
}()

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
