# Bundled Tesseract OCR runtime (Windows x64)

These files are redistributed unmodified from the Tesseract 5.4.0 Windows
build by the University of Mannheim library (UB Mannheim), with one exception
noted below.

- Source: https://github.com/UB-Mannheim/tesseract/releases/tag/v5.4.0.20240606
  (installer `tesseract-ocr-w64-setup-5.4.0.20240606.exe`)
- Tesseract OCR: Apache License 2.0 (see `LICENSE`, authors in `AUTHORS`)
  https://github.com/tesseract-ocr/tesseract
- `tessdata/eng.traineddata`: Apache License 2.0, from tessdata_fast
  https://github.com/tesseract-ocr/tessdata_fast
- Runtime libraries shipped with that build (MSYS2/MinGW-w64 packages):
  Leptonica (BSD-2-Clause), libjpeg (IJG), libpng (PNG Reference Library
  License), libtiff (libtiff License), libwebp and libsharpyuv (BSD-3-Clause),
  OpenJPEG (BSD-2-Clause), giflib (MIT), zlib, libdeflate (MIT), xz/liblzma
  (0BSD/public domain), zstd (BSD-3-Clause), lz4 (BSD-2-Clause), bzip2
  (bzip2 License), libarchive (BSD-2-Clause), libb2/BLAKE2 (CC0), LERC
  (Apache-2.0), jbigkit (GPL-2.0-or-later, dynamically linked and unmodified),
  expat (MIT), libiconv (LGPL-2.1-or-later, dynamically linked and unmodified),
  OpenSSL libcrypto (Apache-2.0), GCC runtime libraries libstdc++, libgcc and
  libwinpthread (GPL-3.0 with GCC Runtime Library Exception / MIT).

Modification: `libtesseract-5.dll` had its DWARF debug sections removed with
`tools/pestrip` to shrink it from 101 MB to about 3 MB. No code was changed.

Files that are only needed for training (ICU, Pango, Cairo, GLib, the
`*training*.exe` tools) and `osd.traineddata` are not included.

To upgrade: install a newer UB Mannheim build, run
`go run ./tools/peinfo <install dir> tesseract.exe` to list the runtime
closure, copy those files plus `tessdata/eng.traineddata`, strip
`libtesseract-5.dll` with `go run ./tools/pestrip`, and bump
`tessbundle.Version`.
