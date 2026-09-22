// Command rulesprobe reports, for given pages, how many table rulings and
// images the layout extractor finds among the page objects, and whether the
// page has a text layer. Useful to judge scanned/outline PDFs.
//
//	go run ./tools/rulesprobe <file.pdf> <page>...
package main

import (
	"fmt"
	"os"
	"strconv"

	"pdf2word/internal/pdflayout"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: rulesprobe file.pdf page...")
		os.Exit(2)
	}
	for _, a := range os.Args[2:] {
		n, _ := strconv.Atoi(a)
		info, err := pdflayout.Probe(os.Args[1], n)
		if err != nil {
			fmt.Printf("page %d: %v\n", n, err)
			continue
		}
		fmt.Printf("page %d: %.0fx%.0f pt, chars=%d rules=%d (h=%d v=%d) tables=%d images=%d\n",
			n, info.Width, info.Height, info.Chars, info.Rules, info.HRules, info.VRules, info.Tables, info.Images)
	}
}
