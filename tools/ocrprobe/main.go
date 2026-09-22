// Command ocrprobe runs the OCR layout pipeline on one page step by step and
// prints what each stage produces. Diagnostic only.
//
//	go run ./tools/ocrprobe <file.pdf> <page>
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"pdf2word/internal/model"
	"pdf2word/internal/ocr"
	"pdf2word/internal/pdflayout"
	"pdf2word/internal/render"
	"pdf2word/internal/tessbundle"
)

func main() {
	path := os.Args[1]
	page, _ := strconv.Atoi(os.Args[2])

	r, err := render.Open(path, 300)
	check(err)
	defer r.Close()
	imgs, err := r.PageImages(page)
	check(err)
	fmt.Printf("render: %dx%d px, %d bytes\n", imgs[0].Width, imgs[0].Height, len(imgs[0].Data))

	if tessbundle.Available() {
		ocr.Bundled = tessbundle.Path
	}
	tp, err := ocr.Find("")
	check(err)
	words, err := (&ocr.Tesseract{Path: tp}).RecognizeWords(context.Background(), imgs[0].Data, imgs[0].Ext)
	check(err)
	fmt.Printf("tesseract: %d words\n", len(words))
	for i, w := range words {
		if i >= 6 {
			break
		}
		fmt.Printf("  %q left=%d top=%d w=%d h=%d lineTop=%d lineH=%d conf=%.0f\n", w.Text, w.Left, w.Top, w.Width, w.Height, w.LineTop, w.LineHeight, w.Conf)
	}

	res, err := pdflayout.ExtractAll(path)
	check(err)
	assets := res.Assets[page]
	pw, ph := res.Doc.Pages[page-1].Width, res.Doc.Pages[page-1].Height
	fmt.Printf("page: %.0fx%.0f pt, assets=%v\n", pw, ph, assets != nil)

	scale := pw / float64(imgs[0].Width)
	var lw []pdflayout.Word
	for _, w := range words {
		lw = append(lw, pdflayout.Word{
			Text: w.Text,
			X0:   float64(w.Left) * scale, X1: float64(w.Left+w.Width) * scale,
			Y1: ph - float64(w.LineTop)*scale, Y0: ph - float64(w.LineTop+w.LineHeight)*scale,
			Size: float64(w.LineHeight) * scale * 1.05,
		})
	}
	if len(lw) > 0 {
		fmt.Printf("first word in points: %+v\n", lw[0])
	}
	laid, setup := pdflayout.AssembleOCR(page, pw, ph, lw, assets)
	fmt.Printf("assembled: %d blocks, source=%v, setup=%+v\n", len(laid.Blocks), laid.Source, setup)
	for i, b := range laid.Blocks {
		if i >= 12 {
			break
		}
		txt := b.Text()
		if len(txt) > 90 {
			txt = txt[:90] + "…"
		}
		extra := ""
		if b.Kind == model.Table {
			extra = fmt.Sprintf(" cols=%v rows=%d", b.Table.ColWidths, len(b.Table.Rows))
		}
		fmt.Printf("  %d: %s align=%s lines=%d runs=%d%s %q\n", i, b.Kind, b.Align, len(b.Lines), countRuns(b), extra, txt)
	}
}

func countRuns(b model.Block) int {
	n := 0
	for _, l := range b.Lines {
		for _, s := range l.Segments {
			n += len(s.Runs)
		}
	}
	return n
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
