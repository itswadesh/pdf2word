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

## 11.3 Bundled Tesseract (2026-09-22, user request)

The user copied the exe to another server and got "tesseract executable not
found". `internal/tessbundle` now embeds (`go:embed`, Windows builds only)
the runtime closure of the UB Mannheim Tesseract 5.4.0 build: `tesseract.exe`,
its 26 DLLs and `tessdata/eng.traineddata`, about 27 MB. `libtesseract-5.dll`
is stripped of 98 MB of DWARF debug sections with `tools/pestrip` (a small
PE rewriter, since no binutils exist on the box); the closure was computed
with `tools/peinfo` from PE import tables. ICU, Pango/Cairo/GLib and the
training tools are not needed at runtime and are omitted.

`tessbundle.Path()` unpacks the tree once per user into
`%LOCALAPPDATA%\pdf2word\tesseract-<Version>\` (temp dir + rename, stamp
file) and returns the exe path. `ocr.Find` consults a `Bundled` hook after
`-tesseract`/`TESSERACT_CMD` and before PATH, so the bundle is the default
but remains overridable. Non-Windows builds compile the same API with
`Available() == false`. Licences are listed in `win64/NOTICE.md`.

## 13. Layout preservation, phase 1: text PDFs (2026-09-22, user request) — implemented

The user reported "formatting is not maintained" and chose (a) an editable,
structured Word document rather than a picture-perfect replica, and (b) text
PDFs first, scanned pages later. Sample: a one-page government certificate
(landscape A4) with a centred bold title, a logo and a QR code, left/right
paired fields, a nine-column ruled table and a footer with items at both
edges. v1 flattened all of that into plain paragraphs.

### 13.1 Engine

Text-layer extraction moves from `ledongthuc/pdf` to PDFium (go-pdfium,
already embedded for rendering). Per page PDFium provides: page size and
rotation; every character with its box, font size, font name, weight and
flags (`GetPageTextStructured`); page objects with bounds, of which path
objects expose their segments and draw mode (table rulings) and image
objects render to bitmaps. A shared `internal/pdfiumx` helper owns the pool
and document opening for both `render` and the new `internal/pdflayout`.
`pdftext` (ledongthuc) stays as a fallback when PDFium cannot open a file.

### 13.2 Document model v2 (`internal/model`)

```go
type Page  struct { Number int; Source PageSource; Width, Height float64 /*pt*/; Blocks []Block }
type Block struct {
    Kind BlockKind            // Paragraph, Heading, Table, Image
    Level int                 // heading level
    Align Alignment           // Left, Center, Right, Justify
    IndentLeft, FirstIndent float64 // pt
    SpaceBefore float64       // pt, vertical gap to the previous block
    Lines []Line              // Paragraph/Heading content; hard breaks between lines
    Table *Table
    Image *Image
}
type Line  struct { Segments []Segment }          // >1 segment = tab-separated columns on one line
type Segment struct { X float64; Runs []Run }      // X: left edge in pt from the page's left margin
type Run   struct { Text string; Bold, Italic bool; Size float64; Font string }
type Table struct { ColWidths []float64; Rows [][]Cell; Ruled bool }
type Cell  struct { Lines []Line; Align Alignment }
type Image struct { Data []byte; Ext string; Width, Height float64 /*pt*/; Align Alignment }
```
`model.Para(text)` builds a plain paragraph; `Block.Text()` joins runs.
OCR output and the ledongthuc fallback keep producing plain paragraphs.

### 13.3 Reconstruction rules (`internal/pdflayout`)

1. **Chars → lines.** Group characters by baseline (|Δbottom| ≤ 0.5 × size)
   and sort by x. Split a line into **segments** at horizontal gaps wider
   than `max(2.5 × space width, 1.2 × size)`; split **runs** on font, size,
   bold or italic change; insert spaces at gaps > 0.25 × size.
2. **Fonts.** Strip subset prefixes (`ABCDEF+`); family = name up to `-` or
   `,`; bold if name contains Bold/Black/Heavy/Semibold or weight ≥ 600;
   italic if name contains Italic/Oblique or the italic flag is set. Map
   standard names: Helvetica/Arial → Arial, Times → Times New Roman,
   Courier → Courier New, Symbol/ZapfDingbats → as is; others pass through.
3. **Rulings.** Path objects that are thin (≤ 2 pt) and long (≥ 6 pt), or
   stroked rectangles, become horizontal/vertical rules. Vertical rules are
   clustered by x, horizontal by y (tolerance 1.5 pt).
4. **Tables.** A lattice of ≥ 2 vertical and ≥ 2 horizontal rules whose
   spans overlap forms a table region; distinct rule positions give column
   and row boundaries; each text segment goes to the cell containing its
   centre. Cells keep their lines; cell alignment centre/right is inferred
   like paragraphs. Tables are ruled (`Ruled: true` → borders).
5. **Images.** Image objects with both sides ≥ 8 pt become Image blocks
   sized by their bounds (pt) using `FPDFImageObj_GetRenderedBitmap`
   converted to PNG; an image covering ≥ 90 % of the page on a page that has
   text is skipped (scanned letterhead backgrounds).
6. **Flow.** Non-table lines, tables and images are ordered top-to-bottom by
   their top edge. Multi-column article layouts are not detected (known
   limitation; reading order becomes row-wise).
7. **Paragraphs.** Consecutive single-segment lines merge when the vertical
   gap ≤ 1.6 × size, size changes < 15 %, and the left edges agree within
   1.5 pt (or the first line is indented). Lines with ≥ 2 segments are
   paragraphs of their own (columns become tab stops at each segment's X; a
   last segment ending within 6 pt of the right margin becomes a right tab).
8. **Alignment.** Per paragraph: centre when every line's centre is within
   2 % of page width of the content centre and no line spans > 85 % of the
   content width; right when right edges align within 2 pt to the right
   margin and left edges vary; justify when ≥ 2 lines have both edges on the
   content edges; otherwise left. Indent = left edge − left margin.
9. **Hard breaks.** Inside a paragraph, a non-final line that ends short of
   the paragraph's widest line by > 30 % ends with a line break (addresses,
   signature blocks, lists).
10. **Headings.** As before (size ratio to the page's dominant size), now on
    top of explicit run formatting; heading paragraphs keep their alignment.
11. **Spacing.** `SpaceBefore` = vertical gap to the previous block minus
    one line height, clamped to [0, 60] pt, so vertical rhythm survives.
12. **Page setup.** Page size from the first page; landscape when width >
    height; margins = the smallest text/table/image edge offsets over all
    pages, clamped to [0.3 in, 1.25 in]. Running headers/footers are not
    separated out (they remain body paragraphs).

### 13.4 DOCX writer additions

Run properties (`rFonts`, `b`, `i`, `sz`), paragraph properties (`jc`,
`ind`, `spacing` before/after, `tabs`), `<w:br/>` for hard breaks, fixed
layout tables with grid, cell widths and single borders, inline pictures
(`w:drawing`/`wp:inline` with `media/imageN.png`, relationship and content
type), and `sectPr` with size, orientation and margins. Element order follows
the OOXML schema. A paragraph always follows a table.

### 13.4.1 Refinements after visual comparison

Rendering the .docx through LibreOffice (obtained for QA at
`C:\tools\lo`) and comparing with the originals led to:

- **Exact leading.** Each paragraph records its baseline-to-baseline
  distance (`Block.Leading`, ≥ 1.15 × size); the writer emits
  `w:line`/`lineRule="exact"`, and gaps between blocks are computed from
  baselines (`SpaceBefore` = previous bottom − this top in Word terms, first
  block measured from the top margin). Pages keep their original height.
- **Merged cells.** A column boundary with no vertical ruling inside a row
  merges the neighbouring cells (`Cell.Span`, `w:gridSpan`).
- **Ruling colour.** Table borders take the most common ruling colour;
  white or transparent shapes are not rulings.
- **Image bands.** Images sharing a horizontal band form one line with a
  centre tab for a centred image and a right tab for one at the right edge
  (`Run.Image`, `Segment.CenterX`); centred text segments in multi-segment
  lines use centre tabs as well.

### 13.5 Verification

Unit tests on synthetic fixtures generated by `tools/genfixtures`
(`layout.pdf`: centred bold title, key/value line, ruled 3×2 table, image,
footer with left/right items, landscape) read back through PDFium; docx
tests parse the XML for `jc`, `b`, `tbl`, `drawing`, `pgSz`. Manual check on
the certificate; if LibreOffice can be obtained, render the .docx to PDF and
compare visually.

## 14. Layout preservation, phase 2: OCR'd pages (2026-09-22) — implemented

Running the 247-page tender ("HMA insourcing.pdf", vector-outline text) after
phase 1 still gave plain paragraphs, because OCR pages bypassed the layout
pipeline. Phase 2 feeds OCR output through the same assembly:

- **Word boxes.** `ocr.Tesseract.RecognizeWords` runs Tesseract with the
  `tsv` config and parses level-4 (line) and level-5 (word) rows into
  `ocr.Word{Text, Left, Top, Width, Height, LineTop, LineHeight, Conf}`.
  Engines that can do this implement `ocr.WordEngine`.
- **Shared assembly.** `pdflayout.assemble` is the page builder for both
  sources. `ExtractAll` returns, for pages without a text layer, their
  `PageAssets` (size, rulings, images), and `AssembleOCR(page, w, h, words,
  assets)` lays OCR words out with them. Vector rulings therefore still
  yield tables on outline PDFs (page 1 of the tender: 80 rulings, one
  table); on raster scans there are none (image-based line detection is a
  future step). Full-page scan images are never embedded.
- **Geometry.** `convert.ocrLayout` maps pixel boxes to points with
  `scale = pageWidth / imageWidth`, uses the *line* box for the vertical
  extent of every word (word boxes vary with ascenders), and estimates the
  font size as `lineHeight × 1.05`. OCR uses a 5 pt edge tolerance instead
  of 2 pt. Bold/italic are not available from Tesseract and stay off.
- **Glyph outlines vs rulings.** Text drawn as paths produces thin filled
  shapes ("l", "I", "-"); filled shapes now need ≥ 15 pt of length to count
  as rulings (stroked lines keep 6 pt).
- **Fallback.** Engines without word boxes, or pages whose size is
  unknown, keep the plain `TextToBlocks` path.

### 14.1 Refinements from the tender comparison

- **Path geometry.** PDFium returns path segment points in the object's own
  space; they are mapped through `FPDFPageObj_GetMatrix`. Word's PDF output
  draws table borders as even-odd filled frames (one path per row holding
  the outer box and cell boxes): filled paths with ≥ 2 rectangular
  sub-paths contribute every rectangle edge as a ruling. Short filled
  pieces (down to 3 pt) are chained by `cluster` before fill-only lines
  shorter than 15 pt are dropped as glyph stems. A lattice of exactly one
  cell (page frame, boxed note) is not a table.
- **OCR noise.** `convert.ocrWords` drops words with confidence < 20,
  dotted-leader garbage (`^[.·…:;,'\x60~_-]{3,}$` or ≥ 6 chars of only
  `c/e/o`/dots), line boxes taller than 12 % of the page, and tall thin
  boxes (page borders read as "|"). Sizes snap to the page's median line
  height (word-weighted) unless a line deviates by more than 25 %, and are
  clamped to 5–40 pt.
- **Segmentation.** OCR lines split into columns at gaps > 1.6 × size
  (text: 1.0); Tesseract's paragraph ids stop merges across paragraphs and
  allow up to 2.2 × size inside one (1.5/2.0 line spacing). Segments of one
  line that land in the same table cell are joined with spaces.
- **Result.** On the 247-page tender: page 1's 10-row key/value table with
  clean cells, the index with right-aligned page numbers, clause pages with
  numbered hanging-indent paragraphs and centred page numbers.

## 12. Dependencies

| Module | Purpose | Licence |
|---|---|---|
| `github.com/ledongthuc/pdf` | text-layer extraction (pure Go) | MIT |
| `github.com/klippa-app/go-pdfium` v1.20 | page rendering (PDFium as WebAssembly via wazero) | MIT (PDFium: BSD-3) |
| `github.com/pdfcpu/pdfcpu` v0.15.0 | embedded image extraction (fallback); fixture generation | Apache-2.0 |
| `golang.org/x/image` | TIFF header decoding at runtime; fixture rendering | BSD-3 |
| Tesseract ≥ 4 (external binary) | OCR at runtime, optional | Apache-2.0 |

Go 1.27, no cgo.

## 15. Merge with the original pdf2word (2026-09-23) — implemented

The earlier `itswadesh/pdf2word` (poppler + hOCR, Odia default) is absorbed. Ported behaviour:

- **Odia built in.** `ori.traineddata` ships in the Windows bundle and `tesseract-ocr-ori` in the Docker image. `-lang` (or `PDF2WORD_LANG`) accepts `ori`, `eng+ori`, etc.
- **Language fallback.** Before OCR the session asks Tesseract for its installed languages, keeps only those requested that exist, and warns once about the rest. If none exist the conversion fails with the available list.
- **Complex-script output.** The document defaults carry `w:cs` = `Document.ComplexScriptFont` (default Nirmala UI) and `w:bidi="or-IN"`; every run keeps `szCs`/`bCs`/`iCs` twins; per-run fonts set only `ascii`/`hAnsi` so the complex-script font is never overridden by a Latin PDF font.
- **Sparse pass** (`-ocr-sparse`, `Options.SparsePass`). A second Tesseract run with `--psm 11` on the same page image; words with confidence ≥ 60, ≥ 2 letters, whose centre is not inside (or within one word-height of) a word from the normal pass are appended in their own paragraph groups. Off by default (doubles OCR time).
- **`PORT`** overrides the listen port when `-addr` is not given; the container listens on `0.0.0.0:$PORT`.

## 16. Upload limit and page revision (2026-09-23) — implemented

- **Limit.** Uploads are capped at 100 MB by default (`-max-upload` MB). The page learns the limit from `/api/info` and refuses larger files before sending; the server refuses by `Content-Length`, by a body cap with 64 KB framing allowance, and by counting the file bytes written, whichever trips first, answering 413 with the limit in the message. Only file names ending in `.pdf` (case-insensitive) are accepted (415), then the `%PDF-` signature check.
- **Progress from the first byte.** The page renders the busy sheet synchronously on drop and uploads with `XMLHttpRequest` so bytes sent are shown. Overall percent: upload 0–10, reading 10–37, page conversion 37–100. "Opening the PDF" (indeterminate sweep) covers the time PDFium needs to load the file (≈3.5 s for 47 MB).
- **Layout.** One column on phones; from 880 px a two-column desk with the sheet sticky at left and the file tray at right. Headline per user; wordmark "PDF to Word Converter"; explanatory copy shown while the tray is empty.
- **Accessibility.** Stable button label on the sheet, visuals `aria-hidden`, a single polite live region announcing state changes and quarter milestones, `role=progressbar` with `aria-valuetext` per file, file names as `h3`, notices with `role=alert` that stay until dismissed, focus returned to the sheet after Dismiss/Remove, keyboard-operable Stop/Try again/Remove, dark mode, reduced motion, high-contrast tweaks, 44 px touch targets.
- **Resilience.** Failed uploads keep the `File` for "Try again"; polling failures show a banner; running conversions of this browser are re-adopted after a reload.
