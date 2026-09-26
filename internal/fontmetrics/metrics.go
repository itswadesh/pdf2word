// Package fontmetrics measures text in the fonts Word will use, from the
// font files installed on this machine, so that lines kept from a PDF can
// be fitted to their original width before Word lays them out.
package fontmetrics

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

type faceKey struct {
	family       string
	bold, italic bool
}

type face struct {
	mu   sync.Mutex
	f    *sfnt.Font
	buf  sfnt.Buffer
	upem float64
	adv  map[rune]float64 // advance in font units
}

var (
	facesMu sync.Mutex
	faces   = map[faceKey]*face{} // nil = looked for, not available
)

// Measure returns the advance width in points of text set in family
// (bold/italic) at size. ok is false when no font file for that face can
// be found, in which case width is 0.
func Measure(family string, bold, italic bool, size float64, text string) (width float64, ok bool) {
	fc := lookup(family, bold, italic)
	if fc == nil || size <= 0 {
		return 0, false
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	units := 0.0
	for _, r := range text {
		units += fc.advance(r)
	}
	return units / fc.upem * size, true
}

// Available reports whether the face can be measured on this machine.
func Available(family string, bold, italic bool) bool {
	return lookup(family, bold, italic) != nil
}

// missingAdvance is the width, in ems, guessed for a letter the font does
// not have: the average letter of its script in Nirmala UI, which Word sets
// the Indian scripts in (measured on running Hindi and Odia text).
func missingAdvance(r rune) float64 {
	switch {
	case r >= 0x0900 && r <= 0x097F: // Devanagari
		return 0.52
	case r >= 0x0B00 && r <= 0x0B7F: // Odia
		return 0.58
	}
	return 0.55
}

func (fc *face) advance(r rune) float64 {
	if a, ok := fc.adv[r]; ok {
		return a
	}
	a := 0.0
	gi, err := fc.f.GlyphIndex(&fc.buf, r)
	switch {
	case err != nil || gi == 0:
		// Not in the font, so not its missing-glyph box either: Word takes
		// the letter from another font. A combining mark or a joiner sits
		// on its letter and adds no width.
		if !unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf) {
			a = missingAdvance(r) * fc.upem
		}
	default:
		ppem := fixed.Int26_6(int(fc.upem) << 6)
		if adv, err := fc.f.GlyphAdvance(&fc.buf, gi, ppem, font.HintingNone); err == nil {
			a = float64(adv) / 64
		}
		if a == 0 && r != ' ' && !unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf) {
			a = 0.5 * fc.upem // a glyph without an advance: an average width
		}
	}
	fc.adv[r] = a
	return a
}

func lookup(family string, bold, italic bool) *face {
	key := faceKey{strings.ToLower(strings.TrimSpace(family)), bold, italic}
	facesMu.Lock()
	defer facesMu.Unlock()
	if fc, seen := faces[key]; seen {
		return fc
	}
	var fc *face
	for _, name := range fileNames(key.family, bold, italic) {
		if p := findFile(name); p != "" {
			if f := load(p); f != nil {
				fc = f
				break
			}
		}
	}
	faces[key] = fc
	return fc
}

func load(path string) *face {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var f *sfnt.Font
	if strings.HasSuffix(strings.ToLower(path), ".ttc") {
		c, err := sfnt.ParseCollection(data)
		if err != nil {
			return nil
		}
		if f, err = c.Font(0); err != nil {
			return nil
		}
	} else if f, err = sfnt.Parse(data); err != nil {
		return nil
	}
	upem := float64(f.UnitsPerEm())
	if upem <= 0 {
		return nil
	}
	return &face{f: f, upem: upem, adv: map[rune]float64{}}
}

// fileNames lists candidate font files for a face, Windows names first,
// then the metric-compatible free fonts Linux distributions ship.
func fileNames(family string, bold, italic bool) []string {
	style := func(reg, b, i, bi string) []string {
		switch {
		case bold && italic:
			return []string{bi}
		case bold:
			return []string{b}
		case italic:
			return []string{i}
		}
		return []string{reg}
	}
	var names []string
	add := func(n ...string) { names = append(names, n...) }
	switch family {
	case "times new roman", "times", "liberation serif":
		add(style("times.ttf", "timesbd.ttf", "timesi.ttf", "timesbi.ttf")...)
		add(style("LiberationSerif-Regular.ttf", "LiberationSerif-Bold.ttf", "LiberationSerif-Italic.ttf", "LiberationSerif-BoldItalic.ttf")...)
	case "arial", "helvetica", "liberation sans":
		add(style("arial.ttf", "arialbd.ttf", "ariali.ttf", "arialbi.ttf")...)
		add(style("LiberationSans-Regular.ttf", "LiberationSans-Bold.ttf", "LiberationSans-Italic.ttf", "LiberationSans-BoldItalic.ttf")...)
	case "courier new", "courier", "liberation mono":
		add(style("cour.ttf", "courbd.ttf", "couri.ttf", "courbi.ttf")...)
		add(style("LiberationMono-Regular.ttf", "LiberationMono-Bold.ttf", "LiberationMono-Italic.ttf", "LiberationMono-BoldItalic.ttf")...)
	case "calibri", "carlito":
		add(style("calibri.ttf", "calibrib.ttf", "calibrii.ttf", "calibriz.ttf")...)
		add(style("Carlito-Regular.ttf", "Carlito-Bold.ttf", "Carlito-Italic.ttf", "Carlito-BoldItalic.ttf")...)
	case "cambria", "caladea":
		add(style("cambria.ttc", "cambriab.ttf", "cambriai.ttf", "cambriaz.ttf")...)
		add(style("Caladea-Regular.ttf", "Caladea-Bold.ttf", "Caladea-Italic.ttf", "Caladea-BoldItalic.ttf")...)
	case "georgia":
		add(style("georgia.ttf", "georgiab.ttf", "georgiai.ttf", "georgiaz.ttf")...)
	case "verdana":
		add(style("verdana.ttf", "verdanab.ttf", "verdanai.ttf", "verdanaz.ttf")...)
	case "tahoma":
		add(style("tahoma.ttf", "tahomabd.ttf", "tahoma.ttf", "tahomabd.ttf")...)
	case "segoe ui":
		add(style("segoeui.ttf", "segoeuib.ttf", "segoeuii.ttf", "segoeuiz.ttf")...)
	case "garamond":
		add(style("gara.ttf", "garabd.ttf", "garait.ttf", "garabd.ttf")...)
	case "consolas":
		add(style("consola.ttf", "consolab.ttf", "consolai.ttf", "consolaz.ttf")...)
	case "trebuchet ms":
		add(style("trebuc.ttf", "trebucbd.ttf", "trebucit.ttf", "trebucbi.ttf")...)
	case "book antiqua":
		add(style("bkant.ttf", "antquab.ttf", "antquai.ttf", "antquabi.ttf")...)
	case "century gothic":
		add(style("gothic.ttf", "gothicb.ttf", "gothici.ttf", "gothicbi.ttf")...)
	case "palatino linotype":
		add(style("pala.ttf", "palab.ttf", "palai.ttf", "palabi.ttf")...)
	}
	return names
}

var (
	dirsOnce sync.Once
	dirs     []string
	indexMu  sync.Mutex
	index    map[string]string // lower-case base name -> path (Linux font tree)
)

func fontDirs() []string {
	dirsOnce.Do(func() {
		if runtime.GOOS == "windows" {
			if w := os.Getenv("WINDIR"); w != "" {
				dirs = append(dirs, filepath.Join(w, "Fonts"))
			}
			if la := os.Getenv("LOCALAPPDATA"); la != "" {
				dirs = append(dirs, filepath.Join(la, "Microsoft", "Windows", "Fonts"))
			}
		} else {
			dirs = append(dirs, "/usr/share/fonts", "/usr/local/share/fonts")
			if home, err := os.UserHomeDir(); err == nil {
				dirs = append(dirs, filepath.Join(home, ".fonts"), filepath.Join(home, ".local", "share", "fonts"))
			}
			if runtime.GOOS == "darwin" {
				dirs = append(dirs, "/Library/Fonts", "/System/Library/Fonts")
			}
		}
	})
	return dirs
}

// findFile locates a font file by base name: directly in the font
// directories on Windows, through a one-time index of the font tree
// elsewhere.
func findFile(name string) string {
	for _, d := range fontDirs() {
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	if runtime.GOOS == "windows" {
		return ""
	}
	indexMu.Lock()
	defer indexMu.Unlock()
	if index == nil {
		index = map[string]string{}
		for _, d := range fontDirs() {
			filepath.WalkDir(d, func(p string, e os.DirEntry, err error) error {
				if err == nil && !e.IsDir() {
					index[strings.ToLower(e.Name())] = p
				}
				return nil
			})
		}
	}
	return index[strings.ToLower(name)]
}
