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
- `pdfimage.PageImages(path string, page int) ([]Image, error)` uses pdfcpu
  `ExtractImagesRaw` and returns `{Data []byte, Ext string, Width, Height}`.
  Images smaller than 50 px on either side are skipped (rules, icons).

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
  -v                 verbose progress to stderr
  -version           print version
```

Exit codes: `0` success, `1` conversion error, `2` usage error. On success a
one-line summary is printed: pages converted, pages OCR'd, warnings count.

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

## 12. Dependencies

| Module | Purpose | Licence |
|---|---|---|
| `github.com/ledongthuc/pdf` | text-layer extraction (pure Go) | MIT |
| `github.com/pdfcpu/pdfcpu` v0.15.0 | image extraction; fixture generation | Apache-2.0 |
| `golang.org/x/image` | fixture rendering only | BSD-3 |
| Tesseract ≥ 4 (external binary) | OCR at runtime, optional | Apache-2.0 |

Go 1.27, no cgo.
