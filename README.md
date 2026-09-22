# pdf2word

Turn PDF files into Word (`.docx`) documents. Double-click the program, drop
PDFs on the page that opens, and save the Word files it gives back.

- **Text PDFs** – the text layer is read directly and rebuilt into paragraphs
  and headings (headings are detected from font size).
- **Scanned PDFs and print-to-PDF outlines** – pages without selectable text
  are rendered and read with [Tesseract](https://github.com/tesseract-ocr/tesseract)
  OCR. This includes files where a print driver turned the text into vector
  outlines (for example "Microsoft: Print To PDF").
- Page breaks are preserved so the Word document follows the PDF's pagination.
- Live progress: a page counter and bar while it works, per file.
- One executable, no installer, no cgo. The PDF renderer (PDFium) is built in
  as WebAssembly; only Tesseract is external.

## Using the app

1. Start `pdf2word.exe` (double-click). A console window shows the address and
   your browser opens `http://127.0.0.1:<port>/`.
2. Drop one or more PDFs onto the page (or click the sheet to choose files).
3. Watch the counter. When a file is done its Word document downloads by
   itself; the "Save Word file" button downloads it again.
4. Close the browser tab when finished. The program exits on its own shortly
   after (pass `-no-auto-exit` to keep it running).

Options on the page:

| Option | Meaning |
|---|---|
| Text recognition: *only where needed* (default) | OCR pages with fewer than 20 characters of real text. |
| Text recognition: *off* | Never OCR. Scanned pages come out empty, with a note. |
| Text recognition: *every page* | OCR every page and prefer the OCR text. |
| Language | Tesseract language codes, e.g. `eng`, `eng+hin`. Extra languages need their `traineddata` files installed. |

Everything runs locally. The server listens on the loopback address only,
refuses requests from other hosts or origins, and stores uploads in a
temporary folder that is removed when the program exits.

## Requirements

| Purpose | Requirement |
|---|---|
| Run | Windows, macOS or Linux; a browser |
| OCR (scanned pages) | Tesseract 4 or 5 on your `PATH`, or pass `-tesseract path\to\tesseract.exe` |
| Build from source | Go 1.27 or newer |

Text-only PDFs convert without Tesseract. If a page needs OCR and Tesseract
cannot be found, the file fails with a clear message rather than producing an
empty document.

Installing Tesseract:

- **Windows** – installer from <https://github.com/UB-Mannheim/tesseract/wiki>.
  pdf2word also looks in `%LOCALAPPDATA%\Programs\Tesseract-OCR`,
  `C:\Program Files\Tesseract-OCR` and `C:\tools\Tesseract-OCR`.
- **macOS** – `brew install tesseract`
- **Debian/Ubuntu** – `sudo apt install tesseract-ocr`

## Command line

The same executable converts from a terminal when given a file:

```
pdf2word [flags] input.pdf [output.docx]

  -o string          output .docx path (default: input name with .docx)
  -ocr string        OCR mode: auto, off or force (default "auto")
  -lang string       Tesseract language(s), e.g. eng or eng+deu (default "eng")
  -tesseract string  path to the tesseract executable (default: auto-detect)
  -dpi int           resolution used to render pages before OCR (default 300)
  -jobs int          pages to OCR at the same time (default: CPUs, at most 8)
  -min-text int      text-layer characters below which a page counts as scanned (default 20)
  -v                 verbose: one progress line per page plus diagnostics
  -no-progress       disable the progress indicator
  -addr string       address for the browser page (default "127.0.0.1:0", a free port)
  -no-browser        app mode: do not open the browser automatically
  -no-auto-exit      app mode: keep running after the browser page is closed
  -version           print version and exit
```

```sh
pdf2word invoice.pdf                      # -> invoice.docx
pdf2word -ocr force -lang eng+fra scan.pdf
pdf2word -addr 127.0.0.1:8080 -no-browser # app mode on a fixed port
```

Exit codes: `0` success, `1` conversion failed, `2` bad usage.

## How a page is handled

1. The text layer is extracted and rebuilt into lines, paragraphs and
   headings.
2. If the page has fewer than `-min-text` characters (or OCR is forced), the
   page is rendered at `-dpi` and passed to Tesseract. If rendering is
   unavailable, the images embedded in the page are used instead.
3. Blocks are written as Word paragraphs; a page break separates pages.

Speed: rendering takes about 0.1 s per page and OCR 1 to 2 s per page per
process. Pages needing OCR are processed several at a time (`-jobs`, default
one per CPU up to 8), so a 250-page scanned document takes a couple of
minutes on a multi-core PC. Text PDFs convert in seconds.

## Limitations

- Layout is "readable document", not a pixel-perfect replica: tables,
  multi-column layouts, footnotes, images, fonts and colours are not
  reproduced.
- OCR quality depends on scan quality and language data; dotted leaders and
  tables of contents produce noise.
- Encrypted PDFs are not supported.

## Development

```sh
go test ./...                # unit tests; real-Tesseract tests skip if it is not installed
go vet ./...
go build -o bin/pdf2word.exe ./cmd/pdf2word
go run ./tools/genfixtures   # regenerate testdata/*.pdf
```

Project layout:

```
cmd/pdf2word/        executable: app mode (server + browser) and command line
internal/web/        HTTP API, job queue and the embedded page (static/index.html)
internal/convert/    pipeline orchestration and OCR policy
internal/pdftext/    text-layer extraction and glyph -> line -> paragraph rebuild
internal/render/     page rasteriser (PDFium via WebAssembly, no cgo)
internal/pdfimage/   embedded image extraction (pdfcpu), fallback for OCR
internal/ocr/        Engine interface, Tesseract CLI wrapper, binary discovery
internal/docx/       .docx writer (standard library only)
internal/model/      shared document model (Document, Page, Block)
tools/genfixtures/   generates the PDF fixtures used by tests
docs/superpowers/    design spec and implementation plan
```

Dependencies: [`github.com/ledongthuc/pdf`](https://github.com/ledongthuc/pdf)
(text layer, MIT), [`github.com/klippa-app/go-pdfium`](https://github.com/klippa-app/go-pdfium)
(PDFium rendering through wazero, MIT/BSD),
[`github.com/pdfcpu/pdfcpu`](https://github.com/pdfcpu/pdfcpu) (embedded
images, Apache-2.0), `golang.org/x/image` (TIFF header decoding and fixture
rendering, BSD-3). Tesseract is invoked as an external process.
