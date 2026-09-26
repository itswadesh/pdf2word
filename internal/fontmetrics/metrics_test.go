package fontmetrics

import (
	"math"
	"testing"
)

func TestMeasure(t *testing.T) {
	if !Available("Times New Roman", false, false) {
		t.Skip("Times New Roman (or Liberation Serif) is not installed here")
	}
	narrow, _ := Measure("Times New Roman", false, false, 12, "iiii")
	wide, _ := Measure("Times New Roman", false, false, 12, "mmmm")
	if !(narrow > 0 && wide > 2*narrow) {
		t.Errorf("iiii=%.2f mmmm=%.2f: m must be much wider than i", narrow, wide)
	}
	// "Hello world" in Times at 12 pt is about 56 pt wide.
	w, ok := Measure("Times New Roman", false, false, 12, "Hello world")
	if !ok || w < 50 || w > 62 {
		t.Errorf("Hello world = %.2f pt, ok=%v; want about 56", w, ok)
	}
	if b, _ := Measure("Times New Roman", true, false, 12, "Hello world"); Available("Times New Roman", true, false) && b <= w {
		t.Errorf("bold (%.2f) should be wider than regular (%.2f)", b, w)
	}
	double, _ := Measure("Times New Roman", false, false, 24, "Hello world")
	if double < 1.99*w || double > 2.01*w {
		t.Errorf("width must scale with size: %.2f vs %.2f", double, w)
	}
	if _, ok := Measure("No Such Font Family", false, false, 12, "x"); ok {
		t.Error("unknown family must report not ok")
	}
}

// Letters a font lacks are guessed at their script's average width in
// Nirmala UI (0.52 em for Devanagari, 0.58 em for Odia, 0.55 em otherwise),
// not at the width of the font's missing-glyph box, and a combining mark (a
// virama, a vowel sign above or below) sits on its letter and takes no width
// of its own. "ପ୍ରତି" is ପ, ୍ (mark), ର, ତ and ି (mark): three letters;
// "किराया" is क, ि (a spacing sign), र, ा (spacing), य, ा (spacing): six.
func TestMeasureMissingGlyphs(t *testing.T) {
	if !Available("Times New Roman", false, false) {
		t.Skip("Times New Roman (or Liberation Serif) is not installed here")
	}
	for _, tc := range []struct {
		text string
		want float64
	}{{"ପ୍ରତି", 3 * 5.8}, {"किराया", 6 * 5.2}, {"ক", 5.5}} {
		if w, _ := Measure("Times New Roman", false, false, 10, tc.text); math.Abs(w-tc.want) > 0.01 {
			t.Errorf("%s at 10 pt = %.2f pt, want %.2f", tc.text, w, tc.want)
		}
	}
}
