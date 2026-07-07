# syntax=docker/dockerfile:1

# --- build stage ---------------------------------------------------------
FROM golang:1.23 AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/checksum ./cmd/checksum

# --- final stage: distroless, static, non-root ---------------------------
FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.source="https://github.com/manuxio/file-comparer"
LABEL org.opencontainers.image.description="Forensic checksum manifest generator (App 1)"
LABEL org.opencontainers.image.licenses="MIT"

COPY --from=build /out/checksum /usr/local/bin/checksum

# Sensible defaults; override at runtime. Source disk should be mounted at
# /mnt/data (read-only) and the output directory should be writable.
ENV SCAN_ROOT=/mnt/data \
    OUTPUT=/out/checksums.csv \
    ALGO=sha256

ENTRYPOINT ["/usr/local/bin/checksum"]
