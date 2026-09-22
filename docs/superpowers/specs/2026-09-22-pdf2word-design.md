# pdf2word — PDF to Word (.docx) converter in Go, with OCR

Date: 2026-09-22
Status: approved for implementation (autonomous session; assumptions listed below)

## 1. Goal

A command-line tool, written in Go, that converts a PDF file into a Word `.docx`
file. It must handle two kinds of input:

1. **Text PDFs** – documents with a real text layer. Text is extracted directly.
2. **Scanned PDFs** – pages that are only images. Text is recovered with OCR
   (Tesseract) and written into the document.

Output preserves the reading order, paragraph structure, headings (detected by
font size) and page breaks. The tool builds without cgo and runs on Windows,
macOS and Linux.

## 2. Assumptions made in this session

- "Word" means the modern `.docx` (OOXML) format, not legacy `.doc`.
- A CLI is the deliverable. The conversion logic lives in importable internal
  packages so a library or HTTP front end can be added later.
- OCR uses the Tesseract command-line binary (no cgo). It must be installed
  separately; the tool locates it automatically or via a flag.
- Layout fidelity is "readable document", not "pixel identical". Tables, multi
  column detection, embedded images, fonts, colours and hyperlinks are out of
  scope for v1.

## 3. Non-goals (v1)

- Tables, columns, footnotes, forms, annotations.
- Embedding images from the PDF into the `.docx`.
- Encrypted PDFs (other than ones that open with an empty password).
- Rasterising vector-only pages for OCR (needs an external renderer; see §7).

## 4. Architecture

```
input.pdf
   │
   ▼
pdftext.Extract ──► per-page glyphs ──► lines ──► paragraphs/headings
   │                                                  │
   │  page has (almost) no text?                      │
   ▼                                                  │
pdfimage.PageImages ──► ocr.Engine (tesseract) ──► ocr.TextToBlocks
   │                                                  │
   └──────────────► model.Document ◄──────────────────┘
                          │
                          ▼
                     docx.Write ──► output.docx
```

`internal/convert` orchestrates the pipeline; `cmd/pdf2word` is a thin CLI.

### 4.1 Packages

| Package | Responsibility | Depends on |
|---|---|---|
| `cmd/pdf2word` | Flags, exit codes, logging | `internal/convert` |
| `internal/model` | Intermediate document model (`Document`, `Page`, `Block`) | – |
| `internal/pdftext` | Text-layer extraction and glyph → line → paragraph reconstruction | `ledongthuc/pdf`, `model` |
| `internal/pdfimage` | Extract embedded page images (for OCR) | `pdfcpu`, `model` |
| `internal/ocr` | `Engine` interface, Tesseract CLI implementation, discovery, OCR text → blocks | `os/exec`, `model` |
| `internal/docx` | Write `model.Document` as a valid `.docx` (zip + OOXML) using only the standard library | `archive/zip`, `encoding/xml`, `model` |
| `internal/convert` | Orchestrator: options, OCR decision per page, report | all of the above |
| `tools/genfixtures` | Generates `testdata/*.pdf` fixtures (dev only) | `pdfcpu`, `x/image` |

### 4.2 Document model

```go
type Document struct { Pages []Page }
type Page struct {
    Number int          // 1-based
    Source PageSource   // SourceText | SourceOCR | SourceEmpty
    Blocks []Block
}
type Block struct {
    Kind  BlockKind     // Paragraph | Heading
    Level int           // heading level 1..2 (0 for paragraphs)
    Text  string        // plain text, no trailing newline
}
```

## 5. Text-layer reconstruction (`internal/pdftext`)

`ledongthuc/pdf` returns one item **per glyph** with `X`, `Y`, `W`, `FontSize`.
The pure function `BuildBlocks([]Glyph) []model.Block` performs:

1. Sort glyphs by `Y` descending (PDF origin is bottom-left), then `X` ascending.
2. **Lines** – a glyph joins the current line when `|Y − lineY| ≤ 0.5 × size`.
3. **Words** – within a line, sorted by `X`; insert a space when the horizontal
   gap `cur.X − (prev.X + prev.W)` exceeds `0.25 × size`. Runs of whitespace
   collapse to one space.
4. **Paragraphs** – consecutive lines merge when the vertical gap is
   `≤ 1.6 × size` and font size changes by less than 15 %. Lines are joined with
   a space; a trailing hyphen followed by a lowercase-initial next line is
   removed (de-hyphenation).
5. **Headings** – body size is the most common glyph size on the page. A block
   whose dominant size is `≥ 1.6 ×` body → Heading 1, `≥ 1.25 ×` → Heading 2,
   provided the block is ≤ 200 characters. Otherwise Paragraph.

`Extract(path) (*model.Document, error)` opens the PDF, runs the above per page,
and recovers from library panics per page (malformed content marks the page
`SourceEmpty` with a warning rather than aborting the file). Encrypted files
yield a clear error.

## 6. OCR (`internal/ocr`, `internal/pdfimage`)

```go
type Engine interface {
    Name() string
    Recognize(ctx context.Context, img []byte, ext string) (string, error)
}
```

- `Tesseract{Path, Lang}` writes the image bytes to a temp file and runs
  `tesseract <file> stdout -l <lang>`; stdout is the text; stderr is attached to
  any error. `Available(ctx)` runs `--version`.
- `Find(explicit string) (string, error)` resolves the binary: explicit flag →
  `TESSERACT_CMD` env → `PATH` → well-known install dirs
  (`C:\Program Files\Tesseract-OCR`, `%LOCALAPPDATA%\Programs\Tesseract-OCR`,
  `C:\tools\Tesseract-OCR`, `/usr/local/bin`, `/opt/homebrew/bin`).
- `TextToBlocks(text string) []model.Block` splits OCR output on blank lines,
  joins wrapped lines with spaces, trims, drops empty results.
- `pdfimage.Open(path) (*Reader, error)` parses the PDF once;
  `Reader.PageImages(page int) ([]Image, error)` uses pdfcpu
  `ExtractPageImages` and returns `{Data []byte, Ext string, Width, Height}`.
  When pdfcpu leaves the dimensions at zero they are read from the image
  header (`image.DecodeConfig`). Images known to be smaller than 50 px on
  either side are skipped (rules, icons); unknown sizes are kept.

### 6.1 OCR decision per page (`internal/convert`)

| `-ocr` | Behaviour |
|---|---|
| `auto` (default) | OCR a page when its text layer has fewer than `MinTextChars` (default 20) non-space characters **and** the page has at least one usable image. |
| `off` | Never OCR; scanned pages come out empty (with a warning). |
| `force` | OCR every page that has images and use the OCR text instead of the text layer; pages without images fall back to the text layer. |

If any page needs OCR and no Tesseract binary can be found, conversion fails
fast with an actionable message (install Tesseract, pass `-tesseract`, or use
`-ocr off`). Text-only PDFs never require Tesseract.

## 7. Known limitation and extension point

A scanned page whose content is vector drawing rather than an embedded image
cannot be OCR'd without rasterising it. No pure-Go PDF rasteriser is used in v1.
The `convert` package takes an `ImageSource` interface; a future
`pdftoppm`/`mutool` based source can be plugged in without touching the rest.

## 8. DOCX writer (`internal/docx`)

Standard library only. The zip contains:

- `[Content_Types].xml`
- `_rels/.rels`
- `word/document.xml` – one `<w:p>` per block; headings use
  `<w:pStyle w:val="Heading1|Heading2"/>`; pages are separated by
  `<w:r><w:br w:type="page"/></w:r>`; text is in `<w:t xml:space="preserve">`
  and escaped with `xml.EscapeText`; invalid XML control characters are dropped.
- `word/styles.xml` – Normal, Heading1, Heading2 definitions.
- `word/_rels/document.xml.rels`, `docProps/core.xml`, `docProps/app.xml`.

`Write(w io.Writer, doc *model.Document) error`. The CLI writes to a temp file
next to the target and renames on success so no partial `.docx` is left behind.

## 9. CLI

```
pdf2word [flags] input.pdf [output.docx]

  -o string          output file (default: input name with .docx)
  -ocr string        auto | off | force (default "auto")
  -lang string       Tesseract language(s), e.g. eng or eng+deu (default "eng")
  -tesseract string  path to tesseract executable (default: auto-detect)
  -min-text int      chars below which a page counts as scanned (default 20)
  -v                 verbose: one progress line per page plus diagnostics
  -no-progress       disable the progress indicator
  -version           print version
```

Exit codes: `0` success, `1` conversion error, `2` usage error. On success a
one-line summary is printed to stdout: pages converted, pages OCR'd, empty
pages, elapsed time and the output path. Warnings go to stderr.

### 9.1 Progress indicator (added 2026-09-22 at the user's request)

`convert.Options.OnProgress func(Progress)` is invoked once per page after it
is resolved, with `{Page, Total, Source, OCR}`. The CLI renders it on stderr:

- interactive terminal: one line redrawn in place with `\r`, e.g.
  `[############------------------]  40%  page 4/10  ocr`
- `-v`: one line per page (`page 4/10: ocr`) so diagnostics interleave cleanly
- stderr not a terminal and no `-v`: silent
- `-no-progress`: always silent

Terminal detection uses `os.File.Stat()` mode `ModeCharDevice` (no extra
dependency).

## 10. Error handling

- Missing/unreadable input, non-PDF input, encrypted PDF → error, exit 1.
- Per-page extraction panic → recovered, page marked empty, warning logged.
- Tesseract failure on one image → warning, remaining images still processed.
- Output directory not writable → error before any work starts.

## 11. Testing

- **Unit** (`go test ./...`, no external binaries):
  - `pdftext.BuildBlocks` with synthetic glyphs: word spacing, line grouping,
    paragraph gaps, de-hyphenation, heading detection.
  - `ocr.TextToBlocks`, `ocr.Find` resolution order (using a temp dir on PATH).
  - `docx.Write`: unzip in memory, parse `document.xml`, assert paragraph texts,
    styles and page breaks; assert required parts exist.
  - `convert.Convert` with a fake `Engine` and fake `ImageSource` covering
    `auto`/`off`/`force` and the "needs OCR but no engine" error.
  - `pdftext.Extract` and `pdfimage.PageImages` against committed fixtures.
- **Fixtures** (`testdata/`), generated once by `go run ./tools/genfixtures`
  and committed: `text.pdf` (2 pages, heading + paragraphs) and `scanned.pdf`
  (1 page, a PNG of rendered text, no text layer).
- **Integration**: `TestConvertScannedPDF_Tesseract` runs only when a Tesseract
  binary is found (`t.Skip` otherwise) and asserts the OCR'd text contains the
  fixture's known words.

## 11.1 Revision (2026-09-22, after first real-world file)

A 247-page "Microsoft: Print To PDF" tender document exposed three gaps:

1. **Vector-outline pages.** The file had no fonts and no page images: the
   print driver emitted the text as vector paths, so neither the text layer
   nor embedded-image OCR applied. §7's limitation is now removed:
   `internal/render` rasterises pages with PDFium compiled to WebAssembly
   (go-pdfium + wazero, no cgo) and the render is OCR'd. This is now the
   primary OCR path; `pdfimage` embedded extraction is the fallback when the
   renderer cannot start. `convert.Options.DPI` (default 300) controls it.
2. **pdfcpu validation.** Even relaxed validation rejected the whole file over
   `/Redact` annotations with `/OC [1 0 0]`. `pdfimage.Open` now reads and
   optimises without validating and computes the page count directly.
   Fixture `testdata/badannot.pdf` reproduces the defect.
3. **Warning spam.** A failure to open the image source was reported once per
   page. It is now reported once, with a summary count at the end.

`Progress` gained a `Done` counter so indicators do not depend on pages
completing in order.

### 11.1.1 Parallel OCR

Sequential OCR of the 247-page file measured about 2 s per page (8 minutes
total) on a 16-core machine where Tesseract was the bottleneck. `BuildDocument`
now settles text-layer pages first, then OCRs the remaining pages with
`Options.Jobs` workers (default `min(NumCPU, 8)`, CLI `-jobs`). Each worker
renders (the renderer serialises internally; rendering is cheap) and runs its
own Tesseract process with `OMP_THREAD_LIMIT=1`. Results are applied in
completion order; `Progress.Done` counts up while `Progress.Page` may not.
A missing engine still aborts the whole conversion and cancels other workers.

## 11.2 App mode (2026-09-22, user request)

The user asked for a double-click experience instead of a terminal: a page
with drag-and-drop, a progress bar, and the Word file downloaded when done.

- Running the executable with **no arguments** starts `internal/web` on
  `0.0.0.0:9090` (originally `127.0.0.1:<free port>`; changed at the user's
  request) and opens the default browser on `http://127.0.0.1:9090/`. Arguments keep the
  command-line behaviour. Flags: `-addr`, `-no-browser`, `-no-auto-exit`.
- **API** (JSON): `GET /` page; `GET /api/info` version + OCR availability;
  `POST /api/convert` multipart (`ocr`, `lang`, then `file`) → 202 job;
  `GET /api/jobs`, `GET /api/jobs/{id}`; `POST /api/jobs/{id}/cancel`;
  `GET /api/jobs/{id}/download` (attachment, original name with `.docx`).
  Uploads are streamed to a per-job temp dir after a `%PDF-` signature
  check; non-PDFs get 415, oversize 413 (limit 2 GiB).
- **Jobs** run one at a time (semaphore); others show as queued. Finished
  jobs and their files are pruned after an hour and on exit.
- **Page** (`internal/web/static/index.html`, embedded, no external
  assets): a paper-sheet drop target that becomes the live progress display
  (page counter, highlighter-yellow scan band, fill proportional to
  `done/total`); per-file rows with status sentence, thin bar, Stop,
  "Save Word file" and collapsible notes (warnings). Files uploaded from the
  page download automatically when done; files discovered after a reload
  only offer the button. Polling every 0.5 s while active, 4 s otherwise,
  which doubles as the keep-alive. No footer and no options row: the user
  asked for the engine status, version, text-recognition and language
  controls to be removed. The page uploads only the file; the server applies
  its defaults (auto OCR, `-lang`, `-min-text` from the command line). A
  missing OCR engine surfaces as the file's error message. `GET /api/info`
  and the `ocr`/`lang` upload fields remain in the API for tooling.
- **Lifecycle**: the process exits on its own when the page has been silent
  for 45 s and no job is running, or after 5 minutes if the page never
  opened; `-no-auto-exit` disables this. Ctrl+C always works.
- **Security**: loopback bind; requests whose `Host` is not loopback get 403;
  non-GET requests with a foreign `Origin` get 403; `Cache-Control: no-store`.
- **Network sharing** (user asked "how to expose to network", then "set
  default port to 9090 and default to be accessible by network"): the
  default `-addr` is `0.0.0.0:9090`. Whenever `-addr`
  binds a non-loopback address (`0.0.0.0:PORT`, a LAN IP, or `:PORT`), the
  server runs with `AllowRemote` (any `Host` accepted; the `Origin` check
  stays), auto-exit is disabled, and the console lists this machine's LAN
  URLs. Each browser receives an HttpOnly `pdf2word_client` cookie on first
  visit; `GET /api/jobs` returns only that browser's jobs, while by-id
  endpoints stay open (96-bit random ids). No authentication or TLS; the
  README says to use it on a trusted network and how to open the Windows
  firewall port.

## 12. Dependencies

| Module | Purpose | Licence |
|---|---|---|
| `github.com/ledongthuc/pdf` | text-layer extraction (pure Go) | MIT |
| `github.com/klippa-app/go-pdfium` v1.20 | page rendering (PDFium as WebAssembly via wazero) | MIT (PDFium: BSD-3) |
| `github.com/pdfcpu/pdfcpu` v0.15.0 | embedded image extraction (fallback); fixture generation | Apache-2.0 |
| `golang.org/x/image` | TIFF header decoding at runtime; fixture rendering | BSD-3 |
| Tesseract ≥ 4 (external binary) | OCR at runtime, optional | Apache-2.0 |

Go 1.27, no cgo.
