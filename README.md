# pdf2word

Turn PDF files into Word (`.docx`) documents. Double-click the program, drop
PDFs on the page that opens, and save the Word files it gives back.

- **Text PDFs** – the text layer is read with its layout: fonts, bold and
  italic, sizes, centred/right alignment, indents, left/right field pairs
  (as tab stops), ruled tables (as real Word tables), embedded images, and the
  page size and margins of the original. Headings are detected from font
  size.
- **Scanned PDFs and print-to-PDF outlines** – pages without selectable text
  are rendered and read with [Tesseract](https://github.com/tesseract-ocr/tesseract)
  OCR. This includes files where a print driver turned the text into vector
  outlines (for example "Microsoft: Print To PDF"). Word positions from OCR
  go through the same layout step, so headings, centring, justified text,
  indents and spacing are kept, and tables are rebuilt where the PDF still
  contains their ruling lines. Bold and italic cannot be recovered from OCR.
- Page breaks are preserved so the Word document follows the PDF's pagination.
- Live progress: a page counter and bar while it works, per file.
- One executable, no installer, no cgo. The PDF renderer (PDFium, as
  WebAssembly) and, on Windows, the Tesseract OCR runtime with English and
  Odia (ଓଡ଼ିଆ) language data are built in. Copy the exe to another machine
  and it works.
- Indic and other complex scripts come out readable: the Word document sets
  Nirmala UI as its complex-script font and marks runs with the
  complex-script size and weight twins, so Odia, Hindi, Bengali or Tamil text
  does not turn into boxes.

## Using the app

1. Start `pdf2word.exe` (double-click). A console window shows the addresses
   and your browser opens `http://127.0.0.1:9090/`. Other computers on the
   network can open the address printed in the console, e.g.
   `http://192.168.1.23:9090/` (see "Sharing it on your network").
2. Drop one or more PDFs onto the page (or click the sheet to choose files).
   Only files named `.pdf` and no larger than 100 MB are accepted; others
   are skipped with a notice. The limit can be changed with `-max-upload`.
3. Watch the counter. When a file is done its Word document downloads by
   itself; the "Save Word file" button downloads it again.
4. Close the console window (or press Ctrl+C in it) to stop the program.

The page has no settings: pages with fewer than 20 characters of real text
are read with OCR, in English. To change that for the app, start it with the
corresponding flags, e.g. `pdf2word.exe -lang ori` for Odia scans,
`pdf2word.exe -lang eng+ori` for mixed pages, or `pdf2word.exe -min-text 50`.
The environment variable `PDF2WORD_LANG` sets the same default without a
flag. English and Odia are built in; other languages need their Tesseract
`traineddata` files installed. A requested language that is not installed is
skipped with a warning rather than failing the conversion.

Everything runs locally. The server listens on the loopback address only,
refuses requests from other hosts or origins, and stores uploads in a
temporary folder that is removed when the program exits.

## Sharing it on your network

By default the program listens on port 9090 on every network interface, so
other computers on the same network can use it. The console lists the
addresses to share, for example `http://192.168.1.23:9090/`. The program
keeps running until you close it. Each browser sees only the files it
uploaded (a cookie identifies it); anyone who can reach the address can
convert files, as there is no login. Traffic is plain HTTP, so use it on a
trusted network. If the machine has a public IP address, `0.0.0.0` includes
the internet: bind to a private address instead (`-addr 10.0.0.5:9090`) or
restrict the firewall rule to your office's IP range.

To keep it private to this computer only:

```
pdf2word.exe -addr 127.0.0.1:9090
```

In that local-only mode the program also exits on its own once the browser
page is closed (`-no-auto-exit` keeps it running).

On Windows the firewall blocks incoming connections to new programs. An
administrator must allow the port once, either by accepting the "Windows
Security Alert" prompt that appears on first start or with:

```
netsh advfirewall firewall add rule name="pdf2word" dir=in action=allow protocol=TCP localport=9090
```

To keep it running after you log off, run it as a scheduled task at startup
or wrap it in a service manager such as NSSM.

## Running it as a server (Docker / Dokploy)

The repository has a `Dockerfile` and a `docker-compose.yml`. The image is
Debian with Tesseract and its English and Odia data installed from the
distribution; the PDF engine is inside the Go binary, so nothing else is
needed.

```sh
docker compose up -d --build     # then open http://localhost:9090/
```

The container reads two environment variables: `PORT` (the port to listen
on, default 9090, as Cloud Run, Fly and Render set it) and `PDF2WORD_LANG`
(OCR languages, default `eng+ori`). Both are set in the compose file.

On **Dokploy**: create a *Compose* service, point it at this repository
(branch `main`, compose path `docker-compose.yml`), deploy, then add a domain
to the `pdf2word` service on port 9090. Everything runs on one host; uploads
live in the container's temp space and are removed an hour after conversion.
There is no login, so put the domain behind Dokploy's access controls or a
VPN if the server is reachable from the internet. More OCR languages: add
`tesseract-ocr-<lang>` to the `apt-get install` line in the Dockerfile and
set `PDF2WORD_LANG=eng+<lang>` in the compose `environment`.

## Requirements

| Purpose | Requirement |
|---|---|
| Run on Windows x64 | Nothing else: Tesseract 5.4 (English and Odia) is inside the exe |
| Run on macOS / Linux | Tesseract 4 or 5 on your `PATH` (`brew install tesseract`, `apt install tesseract-ocr`) |
| Build from source | Go 1.27 or newer |

On Windows the bundled Tesseract is unpacked on first start to
`%LOCALAPPDATA%\pdf2word\tesseract-<version>\` (about 29 MB) and reused
afterwards. To use a different Tesseract, for example one with more
languages installed, pass `-tesseract path\to\tesseract.exe` or set
`TESSERACT_CMD`; those always win over the bundled copy. Extra languages for
the bundled copy: put their `.traineddata` files (from
[tessdata_fast](https://github.com/tesseract-ocr/tessdata_fast)) into that
folder's `tessdata` subfolder and start with `-lang eng+hin`.

Licences for the bundled runtime are in
`internal/tessbundle/win64/NOTICE.md`. Text-only PDFs never use Tesseract.

## Command line

The same executable converts from a terminal when given a file:

```
pdf2word [flags] input.pdf [output.docx]

  -o string          output .docx path (default: input name with .docx)
  -ocr string        OCR mode: auto, off or force (default "auto")
  -lang string       Tesseract language(s), e.g. eng, ori or eng+ori (default "eng", env PDF2WORD_LANG)
  -ocr-sparse        second OCR pass that recovers text inside pictures and coloured boxes (about twice the OCR time)
  -tesseract string  path to the tesseract executable (default: auto-detect)
  -dpi int           resolution used to render pages before OCR (default 300)
  -jobs int          pages to OCR at the same time (default: CPUs, at most 8)
  -min-text int      text-layer characters below which a page counts as scanned (default 20)
  -v                 verbose: one progress line per page plus diagnostics
  -no-progress       disable the progress indicator
  -addr string       address for the browser page (default "0.0.0.0:9090", env PORT sets the port; 127.0.0.1:PORT = this computer only)
  -max-upload int    app mode: largest PDF the page accepts, in MB (default 100)
  -no-browser        app mode: do not open the browser automatically
  -no-auto-exit      app mode: keep running after the page is closed (only relevant with a 127.0.0.1 address)
  -version           print version and exit
```

```sh
pdf2word invoice.pdf                      # -> invoice.docx
pdf2word -ocr force -lang eng+fra scan.pdf
pdf2word -lang ori -ocr-sparse odia-book.pdf   # Odia scan, also read boxed headings
pdf2word -addr 127.0.0.1:8080 -no-browser # app mode on a fixed port
```

Exit codes: `0` success, `1` conversion failed, `2` bad usage.

## How a page is handled

1. The text layer is read through PDFium: every character with its position
   and font, every image with its placement, and the lines that draw table
   borders. Characters become lines, lines become paragraphs (wrapped prose is
   re-joined, list items and labels stay separate), and each paragraph gets
   its alignment, indent and spacing from the original geometry. Grids of
   ruling lines become Word tables with the text assigned to cells. Images
   are embedded at their original size.
2. If the page has fewer than `-min-text` characters (or OCR is forced), the
   page is rendered at `-dpi` and passed to Tesseract, which returns every
   word with its position. Those positions, together with any ruling lines
   and images the PDF still has, go through the same layout step as text
   pages.
3. Blocks are written as Word paragraphs, tables and pictures; a page break
   separates pages. The Word page size, orientation and margins follow the
   PDF.

Speed: rendering takes about 0.1 s per page and OCR 1 to 2 s per page per
process. Pages needing OCR are processed several at a time (`-jobs`, default
one per CPU up to 8), so a 250-page scanned document takes a couple of
minutes on a multi-core PC. Text PDFs convert in seconds.

## Limitations

- The result is an editable document that follows the original's structure,
  not a pixel-perfect replica. Not yet handled: tables without ruling lines,
  cells merged vertically, multi-column article layouts (read row by row),
  text colours, running headers and footers (they stay in the body), vector
  drawings other than table rulings, bold/italic on OCR'd pages, and tables
  on raster scans (no ruling lines to read).
- OCR quality depends on scan quality and language data; dotted leaders and
  tables of contents produce noise.
- Encrypted PDFs are not supported.

## History

This codebase replaces the original `pdf2word` (August 2026): a single-file Go
server that ran poppler's `pdftoppm` and Tesseract in hOCR mode, written for
Odia (ଓଡ଼ିଆ) scans. Its history is merged in, and what mattered from it was
ported: Odia language data in the bundled runtime and the Docker image,
`PDF2WORD_LANG` and `PORT` as environment variables, the complex-script font
and run properties in the Word output so Indic text renders, and the sparse
second OCR pass (`-ocr-sparse`) that recovers headings printed inside
coloured boxes. The poppler dependency is gone: PDFium renders the pages.

## Development

```sh
go test ./...                # unit tests; real-Tesseract tests skip if it is not installed
go vet ./...
go build -o bin/pdf2word.exe ./cmd/pdf2word
go run ./tools/genfixtures   # regenerate testdata/*.pdf
```

Diagnostics for layout work: `go run ./tools/rulesprobe file.pdf 1 2`
(rulings, tables and images per page), `go run ./tools/ocrprobe file.pdf 1`
(each stage of the OCR layout for one page), `go run ./tools/pdftrim big.pdf
excerpt.pdf 1-25` (copy a page range with PDFium, also from files stricter
parsers reject), `go run ./tools/renderpages
file.pdf outdir 80 1 2` (pages to PNG). To see a result the way Word will
show it, convert the .docx with LibreOffice headless
(`soffice --headless --convert-to pdf`) and render that PDF the same way.

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
