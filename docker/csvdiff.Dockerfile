# syntax=docker/dockerfile:1

# --- build stage ---------------------------------------------------------
FROM golang:1.23 AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/csvdiff ./cmd/csvdiff

# --- final stage: distroless, static, non-root ---------------------------
FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.source="https://github.com/manuxio/file-comparer"
LABEL org.opencontainers.image.description="Checksum manifest diff tool (App 2)"
LABEL org.opencontainers.image.licenses="MIT"

COPY --from=build /out/csvdiff /usr/local/bin/csvdiff

ENV OUTPUT=/out/changes.csv

ENTRYPOINT ["/usr/local/bin/csvdiff"]
