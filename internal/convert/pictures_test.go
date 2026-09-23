package convert

import (
	"context"
	"strings"
	"testing"

	"pdf2word/internal/model"
	"pdf2word/internal/ocr"
	"pdf2word/internal/pdfimage"
)

// noWordsEngine is an OCR engine that finds nothing, as happens on a page
// that holds an illustration.
type noWordsEngine struct{}

func (noWordsEngine) Name() string                                              { return "fake-empty" }
func (noWordsEngine) Recognize(context.Context, []byte, string) (string, error) { return "", nil }
func (noWordsEngine) RecognizeWords(context.Context, []byte, string) ([]ocr.Word, error) {
	return nil, nil
}

// An illustrated book: pages that are one big picture and no text must keep
// the picture instead of coming out empty.
func TestBuildDocument_PictureOnlyPageKeepsPicture(t *testing.T) {
	img := []pdfimage.Image{{Data: []byte("fake-png"), Ext: "png", Width: 100, Height: 130}}
	fi := &fakeImages{pages: map[int][]pdfimage.Image{1: img}}
	doc, rep, err := BuildDocument(context.Background(), fixture("scanned.pdf"), Options{Engine: noWordsEngine{}, OpenImages: opener(fi)})
	if err != nil {
		t.Fatal(err)
	}
	p := doc.Pages[0]
	var pic *model.Block
	for i := range p.Blocks {
		if p.Blocks[i].Kind == model.Image {
			pic = &p.Blocks[i]
		}
	}
	if pic == nil || pic.Image == nil || len(pic.Image.Data) == 0 {
		t.Fatalf("no picture block on the page:\n%s", describeBlocks(p.Blocks))
	}
	if p.TextChars() != 0 {
		t.Errorf("unexpected text on a picture page:\n%s", describeBlocks(p.Blocks))
	}
	if rep.EmptyPages != 0 {
		t.Errorf("page counted as empty: %+v", rep)
	}
	for _, w := range rep.Warnings {
		if strings.Contains(w, "OCR produced no text") {
			t.Errorf("warning for a picture page: %q", w)
		}
	}
}
