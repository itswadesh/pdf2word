package pdflayout

import (
	"bytes"
	"image"
	_ "image/jpeg" // page renders and embedded images
	_ "image/png"
)

const (
	pictureMidToneFraction = 0.15 // share of mid-grey pixels from which a page counts as a picture
	pictureSampleStep      = 4    // sample every n-th pixel in each direction
)

// LooksLikePicture reports whether a page rendering is a picture (a photo,
// a painted cover, shaded art) rather than a scan of a text page. Text on
// paper is almost entirely near-white and near-black; pictures have a large
// share of mid-tones. Undecodable data counts as text.
func LooksLikePicture(data []byte) bool {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return false
	}
	b := img.Bounds()
	if b.Dx() == 0 || b.Dy() == 0 {
		return false
	}
	var total, mid int
	for y := b.Min.Y; y < b.Max.Y; y += pictureSampleStep {
		for x := b.Min.X; x < b.Max.X; x += pictureSampleStep {
			r, g, bl, _ := img.At(x, y).RGBA()
			lum := (299*r + 587*g + 114*bl) / 1000 // 0..65535
			total++
			if lum > 0x4000 && lum < 0xC000 {
				mid++
			}
		}
	}
	return total > 0 && float64(mid)/float64(total) >= pictureMidToneFraction
}
