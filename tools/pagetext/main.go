// Command pagetext prints the first characters of every page's text, one
// line per page, to compare pagination between a PDF and a rendering of
// its conversion.
//
//	go run ./tools/pagetext file.pdf [chars]
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/klippa-app/go-pdfium/requests"

	"pdf2word/internal/pdfiumx"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: pagetext file.pdf [chars]")
		os.Exit(2)
	}
	n := 50
	if len(os.Args) > 2 {
		n, _ = strconv.Atoi(os.Args[2])
	}
	d, err := pdfiumx.Open(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer d.Close()
	d.Mu.Lock()
	defer d.Mu.Unlock()
	for p := 1; p <= d.Pages; p++ {
		txt, err := d.Instance.GetPageText(&requests.GetPageText{Page: d.Page(p)})
		text := ""
		if err == nil {
			text = strings.Join(strings.Fields(txt.Text), " ")
		}
		if len([]rune(text)) > n {
			text = string([]rune(text)[:n])
		}
		fmt.Printf("%d\t%s\n", p, text)
	}
}
