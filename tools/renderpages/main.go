// Command renderpages renders selected pages of a PDF to PNG files, for
// looking at layouts while developing.
//
//	go run ./tools/renderpages <file.pdf> <outdir> <dpi> <page>...
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"pdf2word/internal/render"
)

func main() {
	if len(os.Args) < 5 {
		fmt.Fprintln(os.Stderr, "usage: renderpages file.pdf outdir dpi page...")
		os.Exit(2)
	}
	dpi, _ := strconv.Atoi(os.Args[3])
	r, err := render.Open(os.Args[1], dpi)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer r.Close()
	os.MkdirAll(os.Args[2], 0o755)
	for _, a := range os.Args[4:] {
		n, _ := strconv.Atoi(a)
		imgs, err := r.PageImages(n)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		out := filepath.Join(os.Args[2], fmt.Sprintf("page-%03d.png", n))
		if err := os.WriteFile(out, imgs[0].Data, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		fmt.Println(out)
	}
}
