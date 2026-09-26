# syntax=docker/dockerfile:1

# ---- build -----------------------------------------------------------------
FROM golang:1.27-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
ARG VERSION=docker
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/pdf2word ./cmd/pdf2word

# ---- runtime ---------------------------------------------------------------
# The PDF engine (PDFium as WebAssembly) is inside the binary; only Tesseract
# is needed from the distribution. English, Odia and Hindi data are
# installed; add tesseract-ocr-<lang> packages for more languages and the page
# offers them too. Debian rather than Alpine: Alpine has no Odia traineddata
# package.
FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends tesseract-ocr tesseract-ocr-eng tesseract-ocr-ori tesseract-ocr-hin ca-certificates curl \
      fonts-liberation fonts-crosextra-carlito fonts-crosextra-caladea \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 10001 --create-home --home-dir /home/pdf2word pdf2word

COPY --from=build /out/pdf2word /usr/local/bin/pdf2word

USER pdf2word
# PORT is what Cloud Run, Fly and Render hand you; the binary listens on
# 0.0.0.0:$PORT when it is set. PDF2WORD_LANG is the OCR language the page
# starts on.
ENV HOME=/home/pdf2word \
    OMP_THREAD_LIMIT=1 \
    PORT=9090 \
    PDF2WORD_LANG=eng
EXPOSE 9090
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD curl -fsS "http://127.0.0.1:${PORT}/api/info" >/dev/null || exit 1

ENTRYPOINT ["pdf2word"]
CMD ["-no-browser"]
