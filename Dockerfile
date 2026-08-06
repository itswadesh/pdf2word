# Static build, so the runtime image carries only poppler + tesseract.
FROM golang:1.21-alpine AS build
WORKDIR /src
COPY go.mod main.go index.html ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /pdf2word .

# Debian, not Alpine: Alpine has no Odia traineddata package.
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      poppler-utils tesseract-ocr tesseract-ocr-ori \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /pdf2word /usr/local/bin/pdf2word
# $PORT is what Cloud Run, Fly and Render hand you; 0.0.0.0 or the container
# only answers itself.
ENV PORT=8080
EXPOSE 8080
USER nobody
ENTRYPOINT ["sh", "-c", "exec pdf2word -addr 0.0.0.0:$PORT \"$@\"", "--"]
