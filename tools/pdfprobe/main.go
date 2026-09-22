// Command pdfprobe dumps what PDFium sees on one page: text runs with font
// information, path objects (table rulings), image objects and form objects.
// Used while designing layout reconstruction.
//
//	go run ./tools/pdfprobe <file.pdf> <page(1-based)>
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/enums"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
)

func main() {
	path := os.Args[1]
	pageNo, _ := strconv.Atoi(os.Args[2])
	pool, err := webassembly.Init(webassembly.Config{MinIdle: 1, MaxIdle: 1, MaxTotal: 1})
	check(err)
	inst, err := pool.GetInstance(30 * time.Second)
	check(err)
	defer inst.Close()
	f, err := os.Open(path)
	check(err)
	defer f.Close()
	st, err := f.Stat()
	check(err)
	doc, err := inst.OpenDocument(&requests.OpenDocument{FileReader: f, FileReaderSize: st.Size()})
	check(err)
	page := requests.Page{ByIndex: &requests.PageByIndex{Document: doc.Document, Index: pageNo - 1}}

	size, err := inst.FPDF_GetPageSizeByIndexF(&requests.FPDF_GetPageSizeByIndexF{Document: doc.Document, Index: pageNo - 1})
	check(err)
	rot, _ := inst.FPDFPage_GetRotation(&requests.FPDFPage_GetRotation{Page: page})
	fmt.Printf("page %d: %.1f x %.1f pt, rotation %v\n", pageNo, size.Size.Width, size.Size.Height, rot.PageRotation)

	fmt.Println("\n=== text rects (same line, same font) with font info ===")
	txt, err := inst.GetPageTextStructured(&requests.GetPageTextStructured{Page: page, Mode: requests.GetPageTextStructuredModeRects, CollectFontInformation: true})
	check(err)
	for i, r := range txt.Rects {
		if i >= 40 {
			fmt.Printf("... %d rects total\n", len(txt.Rects))
			break
		}
		fi := r.FontInformation
		font := ""
		if fi != nil {
			font = fmt.Sprintf("size=%.1f weight=%d name=%q flags=%#x", fi.Size, fi.Weight, fi.Name, fi.Flags)
		}
		s := r.Text
		if len(s) > 50 {
			s = s[:50] + "…"
		}
		fmt.Printf("  [%6.1f %6.1f %6.1f %6.1f] %s | %q\n", r.PointPosition.Left, r.PointPosition.Top, r.PointPosition.Right, r.PointPosition.Bottom, font, s)
	}

	fmt.Println("\n=== page objects ===")
	count, err := inst.FPDFPage_CountObjects(&requests.FPDFPage_CountObjects{Page: page})
	check(err)
	fmt.Printf("%d objects\n", count.Count)
	counts := map[enums.FPDF_PAGEOBJ]int{}
	shownText, shownPath := 0, 0
	var dump func(obj references.FPDF_PAGEOBJECT, depth int)
	dump = func(obj references.FPDF_PAGEOBJECT, depth int) {
		t, _ := inst.FPDFPageObj_GetType(&requests.FPDFPageObj_GetType{PageObject: obj})
		counts[t.Type]++
		b, _ := inst.FPDFPageObj_GetBounds(&requests.FPDFPageObj_GetBounds{PageObject: obj})
		ind := strings.Repeat("  ", depth)
		switch t.Type {
		case enums.FPDF_PAGEOBJ_PATH:
			dm, _ := inst.FPDFPath_GetDrawMode(&requests.FPDFPath_GetDrawMode{PageObject: obj})
			sw, _ := inst.FPDFPageObj_GetStrokeWidth(&requests.FPDFPageObj_GetStrokeWidth{PageObject: obj})
			n, _ := inst.FPDFPath_CountSegments(&requests.FPDFPath_CountSegments{PageObject: obj})
			var pts []string
			for i := 0; i < n.Count && i < 6; i++ {
				seg, _ := inst.FPDFPath_GetPathSegment(&requests.FPDFPath_GetPathSegment{PageObject: obj, Index: i})
				p, _ := inst.FPDFPathSegment_GetPoint(&requests.FPDFPathSegment_GetPoint{PathSegment: seg.PathSegment})
				st, _ := inst.FPDFPathSegment_GetType(&requests.FPDFPathSegment_GetType{PathSegment: seg.PathSegment})
				pts = append(pts, fmt.Sprintf("%v(%.1f,%.1f)", st.Type, p.X, p.Y))
			}
			if shownPath < 40 {
				fmt.Printf("%spath bounds=[%.1f %.1f %.1f %.1f] fill=%v stroke=%v width=%.2f segs=%d %s\n", ind, b.Left, b.Bottom, b.Right, b.Top, dm.FillMode, dm.Stroke, sw.StrokeWidth, n.Count, strings.Join(pts, " "))
				shownPath++
			}
		case enums.FPDF_PAGEOBJ_IMAGE:
			md, err := inst.FPDFImageObj_GetImageMetadata(&requests.FPDFImageObj_GetImageMetadata{ImageObject: obj, Page: page})
			if err == nil {
				fmt.Printf("%simage bounds=[%.1f %.1f %.1f %.1f] %dx%d px bpp=%d cs=%v\n", ind, b.Left, b.Bottom, b.Right, b.Top, md.ImageMetadata.Width, md.ImageMetadata.Height, md.ImageMetadata.BitsPerPixel, md.ImageMetadata.Colorspace)
			} else {
				fmt.Printf("%simage bounds=[%.1f %.1f %.1f %.1f] (metadata: %v)\n", ind, b.Left, b.Bottom, b.Right, b.Top, err)
			}
		case enums.FPDF_PAGEOBJ_TEXT:
			if shownText < 6 {
				f, err := inst.FPDFTextObj_GetFont(&requests.FPDFTextObj_GetFont{PageObject: obj})
				desc := ""
				if err == nil {
					name, _ := inst.FPDFFont_GetBaseFontName(&requests.FPDFFont_GetBaseFontName{Font: f.Font})
					w, _ := inst.FPDFFont_GetWeight(&requests.FPDFFont_GetWeight{Font: f.Font})
					fl, _ := inst.FPDFFont_GetFlags(&requests.FPDFFont_GetFlags{Font: f.Font})
					desc = fmt.Sprintf("font=%q weight=%d italic=%v", name.BaseFontName, w.Weight, fl.Italic)
				} else {
					desc = "font: " + err.Error()
				}
				fmt.Printf("%stext bounds=[%.1f %.1f %.1f %.1f] %s\n", ind, b.Left, b.Bottom, b.Right, b.Top, desc)
				shownText++
			}
		case enums.FPDF_PAGEOBJ_FORM:
			n, _ := inst.FPDFFormObj_CountObjects(&requests.FPDFFormObj_CountObjects{PageObject: obj})
			fmt.Printf("%sform bounds=[%.1f %.1f %.1f %.1f] with %d objects\n", ind, b.Left, b.Bottom, b.Right, b.Top, n.Count)
			for i := 0; i < n.Count; i++ {
				child, err := inst.FPDFFormObj_GetObject(&requests.FPDFFormObj_GetObject{PageObject: obj, Index: uint64(i)})
				if err == nil {
					dump(child.PageObject, depth+1)
				}
			}
		}
	}
	for i := 0; i < count.Count; i++ {
		o, err := inst.FPDFPage_GetObject(&requests.FPDFPage_GetObject{Page: page, Index: i})
		check(err)
		dump(o.PageObject, 0)
	}
	fmt.Printf("\ncounts: text=%d path=%d image=%d shading=%d form=%d\n", counts[enums.FPDF_PAGEOBJ_TEXT], counts[enums.FPDF_PAGEOBJ_PATH], counts[enums.FPDF_PAGEOBJ_IMAGE], counts[enums.FPDF_PAGEOBJ_SHADING], counts[enums.FPDF_PAGEOBJ_FORM])
	_ = pdfium.Pool(nil)
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
