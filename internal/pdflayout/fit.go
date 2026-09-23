package pdflayout

import (
	"math"
	"unicode/utf8"

	"pdf2word/internal/fontmetrics"
	"pdf2word/internal/model"
)

// measureText is the text measurer; tests replace it.
var measureText = fontmetrics.Measure

// defaultBodyFont is the font Word uses for runs that name none (the
// document default in styles.xml).
const defaultBodyFont = "Calibri"

const (
	fitSafety      = 0.006 // fraction of the target width kept in hand: word processors measure a little differently
	fitTolerance   = 0.3   // points a line may exceed its (safe) target before it is fitted
	maxFitSpacing  = 1.0   // points per character that spacing may remove before scaling takes over (spacing is applied exactly by every word processor; horizontal scale is not)
	minFitScale    = 0.75  // narrowest horizontal scale applied
	fitMinGlyphs   = 2     // lines with fewer glyphs are left alone
	fitMinNetWidth = 1.0   // points; measured widths below this are meaningless
)

// fitKeptLine makes a line that keeps the PDF's break fit the width it had
// there when set in the substitute font: a typesetter's justified line is
// often a little narrower than Word's natural setting of the same text
// (compressed word spaces, kerning, a different face), and Word can only
// stretch, so without this the last word would wrap. A letter-spaced line
// keeps its tracking, adjusted to hit the same width exactly.
func fitKeptLine(ml *model.Line, target float64) {
	if len(ml.Segments) != 1 || target <= 0 {
		return
	}
	seg := &ml.Segments[0]
	natural, glyphs := 0.0, 0
	for _, r := range seg.Runs {
		if r.Image != nil || r.Text == "" {
			return
		}
		family := r.Font
		if family == "" {
			family = defaultBodyFont
		}
		w, ok := measureText(family, r.Bold, r.Italic, r.Size, r.Text)
		if !ok {
			return
		}
		natural += w
		glyphs += utf8.RuneCountInString(r.Text)
	}
	if glyphs < fitMinGlyphs || natural < fitMinNetWidth {
		return
	}
	tracking := seg.Runs[0].Spacing
	target *= 1 - fitSafety
	// Spacing that makes the line exactly target wide. Word processors add
	// it between characters, so glyphs-1 times; it is written in twentieths
	// of a point, so negative values round away from zero to stay safe.
	spacing := (target - natural) / float64(glyphs-1)
	scale := 1.0
	switch {
	case tracking > 0:
		// Letter-spaced line: keep it letter-spaced, at the exact width.
		if spacing < 0 {
			spacing = 0
		}
	case natural <= target+fitTolerance:
		return // fits already; justification may stretch it
	case spacing >= -maxFitSpacing:
		// A small squeeze of the character spacing is invisible.
		spacing = math.Floor(spacing*20) / 20
	default:
		spacing = 0
		scale = math.Floor(math.Max(minFitScale, target/natural)*100) / 100
	}
	for i := range seg.Runs {
		seg.Runs[i].Spacing = spacing
		seg.Runs[i].Scale = scale
	}
}
