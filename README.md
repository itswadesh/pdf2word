# pdf2word

Convert PDF files to Word (`.docx`) from the command line, in Go.

- **Text PDFs** – the text layer is read directly and rebuilt into paragraphs
  and headings (headings are detected from font size).
- **Scanned PDFs** – pages that are only images are run through
  [Tesseract](https://github.com/tesseract-ocr/tesseract) OCR.
- Page breaks are preserved so the Word document follows the PDF's pagination.
- Pure Go build (`CGO_ENABLED=0`), single binary, Windows / macOS / Linux.
- Live progress indicator while converting.

```
$ pdf2word report.pdf
[##############################] 100%  page 12/12  ocr
converted 12 page(s) (9 text, 3 via OCR, 0 empty) in 4.812s -> report.docx
```

## Requirements

| Purpose | Requirement |
|---|---|
| Build | Go 1.27 or newer |
| OCR (scanned pages only) | Tesseract 4 or 5 on your `PATH` (or point to it with `-tesseract`) |

Text-only PDFs convert without Tesseract. If a page needs OCR and Tesseract
cannot be found, pdf2word stops with a clear message instead of producing an
empty document (use `-ocr off` to convert anyway).

Installing Tesseract:

- **Windows** – installer from <https://github.com/UB-Mannheim/tesseract/wiki>.
  pdf2word also looks in `%LOCALAPPDATA%\Programs\Tesseract-OCR`,
  `C:\Program Files\Tesseract-OCR` and `C:\tools\Tesseract-OCR`.
- **macOS** – `brew install tesseract`
- **Debian/Ubuntu** – `sudo apt install tesseract-ocr`

Extra languages are Tesseract `traineddata` files; pass them with
`-lang eng+deu`.

## Build

```sh
git clone <this repo> pdf2word
cd pdf2word
go build -o bin/pdf2word ./cmd/pdf2word        # bin/pdf2word.exe on Windows
```

## Usage

```
pdf2word [flags] input.pdf [output.docx]

  -o string          output .docx path (default: input name with .docx)
  -ocr string        OCR mode: auto, off or force (default "auto")
  -lang string       Tesseract language(s), e.g. eng or eng+deu (default "eng")
  -tesseract string  path to the tesseract executable (default: auto-detect)
  -min-text int      text-layer characters below which a page counts as scanned (default 20)
  -v                 verbose: one progress line per page plus diagnostics
  -no-progress       disable the progress indicator
  -version           print version and exit
```

Examples:

```sh
pdf2word invoice.pdf                     # -> invoice.docx
pdf2word -o out/report.docx report.pdf
pdf2word -ocr force -lang eng+fra scan.pdf
pdf2word -tesseract "C:\Tools\Tesseract-OCR\tesseract.exe" scan.pdf
```

Exit codes: `0` success, `1` conversion failed, `2` bad usage.

### How the OCR decision works

For each page pdf2word counts the characters in the PDF text layer:

| `-ocr` | Behaviour |
|---|---|
| `auto` (default) | OCR a page when it has fewer than `-min-text` characters **and** contains at least one embedded image. |
| `off` | Never OCR. Scanned pages come out empty and a warning is printed. |
| `force` | OCR every page that has images and use the OCR text; pages without images fall back to the text layer. |

### Progress indicator

On an interactive terminal a single-line bar is redrawn in place. With `-v`
one line per page is printed instead so diagnostics are never overwritten.
When stderr is not a terminal (for example in CI logs) the indicator is
silent unless `-v` is given. `-no-progress` turns it off entirely. The final
summary always goes to stdout; warnings go to stderr.

## Limitations

- Layout is "readable document", not a pixel-perfect replica: tables,
  multi-column layouts, footnotes, images, fonts and colours are not
  reproduced.
- Scanned pages are OCR'd from the images embedded in the PDF. A page that is
  pure vector drawing with no text layer cannot be OCR'd (no rasteriser is
  bundled); it is reported as a warning.
- Encrypted PDFs are not supported.

## Development

```sh
go test ./...            # unit tests; the real-Tesseract test skips if it is not installed
go vet ./...
go run ./tools/genfixtures   # regenerate testdata/text.pdf and testdata/scanned.pdf
```

Project layout:

```
cmd/pdf2word/        CLI (flags, exit codes, progress indicator)
internal/model/      shared document model (Document, Page, Block)
internal/pdftext/    text-layer extraction and glyph -> line -> paragraph rebuild
internal/pdfimage/   embedded image extraction (pdfcpu)
internal/ocr/        Engine interface, Tesseract CLI wrapper, binary discovery
internal/docx/       .docx writer (standard library only)
internal/convert/    pipeline orchestration and OCR policy
tools/genfixtures/   generates the PDF fixtures used by tests
docs/superpowers/    design spec and implementation plan
```

Dependencies: [`github.com/ledongthuc/pdf`](https://github.com/ledongthuc/pdf)
(text layer, MIT), [`github.com/pdfcpu/pdfcpu`](https://github.com/pdfcpu/pdfcpu)
(images, Apache-2.0), `golang.org/x/image` (TIFF header decoding and fixture
rendering, BSD-3). Tesseract is invoked as an external process; no cgo.
