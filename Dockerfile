# syntax=docker/dockerfile:1

# --- build ---------------------------------------------------------------
# gorm.io/driver/sqlite тягне mattn/go-sqlite3, тож потрібен cgo і gcc.
FROM golang:1.23-bookworm AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ENV CGO_ENABLED=1 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/info-parser ./cmd/info-parser

# --- runtime -------------------------------------------------------------
FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 1000 --create-home --shell /usr/sbin/nologin app

COPY --from=builder /out/info-parser /usr/local/bin/info-parser

# Каталог даних успадкує власника при створенні тому.
RUN mkdir -p /data && chown 1000:1000 /data

USER 1000:1000
WORKDIR /data
VOLUME ["/data"]

# Розклад рахується в локальному часі процесу — без TZ він би спрацьовував за UTC.
ENV DATABASE_PATH=/data/info-parser.db \
    HTTP_ADDR=:8088 \
    TZ=Europe/Kyiv

EXPOSE 8088
ENTRYPOINT ["/usr/local/bin/info-parser"]
