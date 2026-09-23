package pdflayout

import (
	"testing"
	"unicode/utf8"

	"pdf2word/internal/model"
)

// A fake measurer: every glyph is 6 pt wide at 12 pt, whatever the font.
func sixPerGlyph(_ string, _, _ bool, size float64, text string) (float64, bool) {
	return float64(utf8.RuneCountInString(text)) * size / 2, true
}

func TestFitKeptLine(t *testing.T) {
	old := measureText
	measureText = sixPerGlyph
	defer func() { measureText = old }()

	mk := func(text string) model.Line {
		return model.Line{Segments: []model.Segment{{Runs: []model.Run{{Text: text, Size: 12, Font: "Times New Roman"}}}}}
	}
	// 10 glyphs = 60 pt natural.
	l := mk("abcdefghij")
	fitKeptLine(&l, 62)
	if r := l.Segments[0].Runs[0]; r.Spacing != 0 || r.Scale != 0 {
		t.Errorf("a line that fits must be left alone: %+v", r)
	}
	// Targets are met with a small safety margin (fitSafety); spacing is
	// applied between glyphs (9 gaps for 10 glyphs) and rounded away from
	// zero to whole twentieths of a point, hence the values below.
	l = mk("abcdefghij")
	fitKeptLine(&l, 57) // 3 pt too wide: -0.37 pt per gap, rounded to -0.4
	if r := l.Segments[0].Runs[0]; !near(r.Spacing, -0.4, 0.01) || r.Scale != 1 {
		t.Errorf("small excess should become negative spacing: %+v", r)
	}
	l = mk("abcdefghij")
	fitKeptLine(&l, 48) // 12 pt too wide: more than spacing may hide
	if r := l.Segments[0].Runs[0]; r.Spacing != 0 || !near(r.Scale, 0.79, 0.011) {
		t.Errorf("large excess should become horizontal scale 0.79: %+v", r)
	}
	// A tracked line keeps tracking, adjusted to the exact width.
	l = mk("TITLE")
	l.Segments[0].Runs[0].Spacing = 3
	fitKeptLine(&l, 42) // natural 30 + 4 gaps x 3 pt
	if r := l.Segments[0].Runs[0]; !near(r.Spacing, 3, 0.1) || r.Scale != 1 {
		t.Errorf("tracked line = %+v, want spacing 3", r)
	}
	// Two segments (a tabbed line) are not touched.
	two := model.Line{Segments: []model.Segment{{Runs: []model.Run{{Text: "left", Size: 12}}}, {Runs: []model.Run{{Text: "right", Size: 12}}}}}
	fitKeptLine(&two, 10)
	if two.Segments[0].Runs[0].Spacing != 0 {
		t.Error("multi-segment lines must not be fitted")
	}
	// No metrics: nothing happens.
	measureText = func(string, bool, bool, float64, string) (float64, bool) { return 0, false }
	l = mk("abcdefghij")
	fitKeptLine(&l, 10)
	if l.Segments[0].Runs[0].Spacing != 0 || l.Segments[0].Runs[0].Scale != 0 {
		t.Error("without metrics the line must be left alone")
	}
}
