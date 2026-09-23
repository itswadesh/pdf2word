package fontmetrics

import "testing"

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
