# file-comparer

[![build](https://github.com/manuxio/file-comparer/actions/workflows/build.yml/badge.svg)](https://github.com/manuxio/file-comparer/actions/workflows/build.yml)

**English** · [Italiano](README.md)

A **forensic tamper-detection toolkit**: two small, single-purpose Dockerized Go
apps that find which files changed between a compromised/current system state and
the last known-good backup — so a full content diff only has to be run on the
files that actually differ.

- **`checksum`** (App 1) — recursively hashes every file under a root directory
  whose extension matches an allow-list, and writes a deterministic CSV manifest.
- **`csvdiff`** (App 2) — compares two manifests (a baseline and a current) and
  writes a CSV of the files that were **modified**, **added**, or **deleted**.

## Incident-response workflow

```text
1. checksum  → baseline.csv   (run against the last good backup)
2. checksum  → current.csv    (run against the current / suspect system)
3. csvdiff   → changes.csv    (baseline vs current)
4. review changes.csv, then content-diff only the flagged files
```

## CSV formats

**Manifest** (`checksum` output):

```text
absolute_path,filename,last_modified,size_bytes,sha256
```

- `last_modified`: RFC3339 UTC (e.g. `2026-07-07T03:10:00Z`)
- rows are sorted by `absolute_path`, so two runs over identical trees produce
  byte-identical files.

**Changes** (`csvdiff` output):

```text
status,absolute_path,filename,baseline_sha,current_sha,baseline_size,current_size,baseline_modified,current_modified
```

- `status` is `MODIFIED`, `ADDED` (only in current — often the interesting one),
  or `DELETED` (only in baseline). Unchanged files are omitted.

## Docker images

Published to GHCR on every push to `main`:

- `ghcr.io/manuxio/file-comparer/checksum`
- `ghcr.io/manuxio/file-comparer/csvdiff`

Both are `distroless/static:nonroot` images (no shell, no package manager, run as
uid 65532).

### Run `checksum`

```bash
docker run --rm \
  -v /path/to/disk:/mnt/data:ro \
  -v "$PWD/out":/out \
  -e EXTENSIONS=".php,.js,.phtml,.html" \
  -e OUTPUT=/out/current.csv \
  ghcr.io/manuxio/file-comparer/checksum
```

### Run `csvdiff`

```bash
docker run --rm \
  -v "$PWD/out":/data:ro \
  -v "$PWD/out":/out \
  -e BASELINE_CSV=/data/baseline.csv \
  -e CURRENT_CSV=/data/current.csv \
  -e OUTPUT=/out/changes.csv \
  ghcr.io/manuxio/file-comparer/csvdiff
```

> **Permissions:** the source disk is mounted read-only (`:ro`) — the tool never
> modifies the disk under investigation. The output directory must be writable by
> uid 65532; if that is inconvenient for a throwaway analysis, add `--user root`.

### docker-compose

```bash
SCAN_DIR=/path/to/disk OUT_DIR=./out docker compose run --rm checksum
CSV_DIR=./out          OUT_DIR=./out docker compose run --rm csvdiff
```

## Configuration

### `checksum`

| Flag | Env | Default | Meaning |
|---|---|---|---|
| `--root` | `SCAN_ROOT` | `/mnt/data` | Directory tree to scan. |
| `--ext` (repeatable) | `EXTENSIONS` (comma-sep) | *(required)* | Extensions to include, matched case-insensitively. |
| `--output` | `OUTPUT` | *(required)* | Output CSV path. |
| `--algo` | `ALGO` | `sha256` | `sha256` or `sha512`. |
| `--follow-symlinks` | `FOLLOW_SYMLINKS` | `false` | Follow symlinks to regular files. |
| `--fail-fast` | `FAIL_FAST` | `false` | Abort on the first unreadable file. |
| `--max-depth` | `MAX_DEPTH` | `0` | Max directory levels below root to descend (0 = unlimited; root entries are depth 1). Pruned directories are printed to stderr. |
| `--workers` | `WORKERS` | `0` | Hash concurrency (0 = number of CPUs). Lower for a single HDD, higher for NVMe/network. |
| `--dir-workers` | `DIR_WORKERS` | `0` | Directory-read concurrency (0 = same as `--workers`). Raise for high-latency/network filesystems with many directories. |

Exit codes: `0` all good · `1` fatal config/setup error · `2` manifest produced
but some files could not be read (details on stderr).

### `csvdiff`

| Flag | Env | Meaning |
|---|---|---|
| `--baseline` | `BASELINE_CSV` | Baseline (last good backup) manifest. |
| `--current` | `CURRENT_CSV` | Current (suspect) manifest. |
| `--output` | `OUTPUT` | Output changes CSV. |
| `--strip-baseline-prefix` / `--strip-current-prefix` | `STRIP_BASELINE_PREFIX` / `STRIP_CURRENT_PREFIX` | Strip a path prefix before matching, for manifests captured at different mount points. |
| `--fail-on-diff` | `FAIL_ON_DIFF` | Exit `3` when any difference is found. |

Exit codes: `0` success · `1` fatal error · `2` bad input CSV · `3` differences
found (only with `--fail-on-diff`).

## Performance & tuning

Built for multi-TB scans. Both stages run in parallel:

- **Hashing** — a pool of `--workers` goroutines (default = CPU count), each
  streaming files through the hash with a reusable 1 MiB buffer.
- **Directory traversal** — a separate pool of `--dir-workers` goroutines reading
  directories concurrently, so the walk itself isn't a single-threaded bottleneck
  on high-latency or high-parallelism storage. The two stages are decoupled by a
  bounded queue (backpressure keeps memory flat).

Hashing throughput scales near-linearly with workers up to the core count
(measured on ~750 MB / 1500 files, cache-warm):

| `--workers` | elapsed | speedup |
|---|---|---|
| 1  | 510 ms | 1.0× |
| 2  | 247 ms | 2.1× |
| 4  | 125 ms | 4.1× |
| 8  | 69 ms  | 7.4× |
| 16 | 51 ms  | 10×  |

**Rules of thumb:**

- **NVMe / SSD / RAID / network:** raise `--workers` (and `--dir-workers` for
  many-directory trees) — concurrency saturates bandwidth and hides latency.
- **Single spinning HDD:** *lower* `--workers` (try `2`) — parallel reads cause
  head thrashing; near-sequential is faster.
- SHA-256 is hardware-accelerated on modern CPUs, so the workload is almost always
  I/O-bound: the disk, not the CPU, is the ceiling. Match worker count to the
  storage medium.

## Development

No local Go install required if you have Docker:

```bash
docker run --rm -v "$PWD":/src -w /src golang:1.23 go test ./...
docker run --rm -v "$PWD":/src -w /src golang:1.23 go vet ./...
```

With Go 1.23+ installed locally:

```bash
go test ./...
go build ./...
```

See [CLAUDE.md](CLAUDE.md) for architecture/conventions and [PLAN.md](PLAN.md)
for the design decisions and roadmap.
