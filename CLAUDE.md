# CLAUDE.md

Guidance for Claude Code (and humans) working in this repository.

## What this is

A **forensic tamper-detection toolkit**: two small, single-purpose Dockerized Go
apps used to find which files changed between a compromised/current system state
and the last known-good backup, so a full content diff only has to be run on the
handful of files that actually differ.

Typical incident-response flow:

1. Run **`checksum`** (App 1) against the *last good backup* → `baseline.csv`.
2. Run **`checksum`** against the *current (possibly compromised) system* → `current.csv`.
3. Run **`csvdiff`** (App 2) on the two CSVs → `changes.csv` listing every
   added / deleted / modified file.
4. A human reviews `changes.csv` and diffs the content of the flagged files.

The two apps share one CSV schema so App 2 can always consume App 1's output.

## Repository layout

```
.
├── CLAUDE.md                 # this file
├── PLAN.md                   # implementation plan & locked decisions
├── go.mod                    # module: github.com/iqera/file_sha_diff (adjust if needed)
├── cmd/
│   ├── checksum/main.go      # App 1 entrypoint (flag/env parsing, wiring)
│   └── csvdiff/main.go       # App 2 entrypoint
├── internal/
│   ├── csvschema/            # canonical CSV headers + record types (SHARED — the contract)
│   ├── scan/                 # App 1: walk, extension filter, hashing, sorted CSV writer
│   └── compare/              # App 2: load two CSVs, diff by path, emit changes CSV
├── docker/
│   ├── checksum.Dockerfile   # multi-stage → distroless/static, non-root
│   └── csvdiff.Dockerfile
├── docker-compose.yml        # convenience wiring for both apps
└── testdata/                 # sample trees + golden CSVs for tests
```

## The two apps (contract summary)

### App 1 — `checksum`
Recursively scans a root directory, hashes every file whose extension matches an
allow-list, and writes one CSV row per file.

- **Config** (CLI flag *or* env var; flag wins):
  - root dir — `--root` / `SCAN_ROOT` (default `/mnt/data`)
  - extensions — `--ext` (repeatable) / `EXTENSIONS` (comma-separated), e.g. `.php,.js,.phtml`
  - output file — `--output` / `OUTPUT` (path inside container, mapped to a mounted volume)
  - hash algo — `--algo` / `ALGO` (default `sha256`)
- **Output CSV** — see schema below. Rows **sorted by absolute path** (deterministic).
- **Errors:** config problems fail fast (exit 1). Per-file read errors are
  **always surfaced** to stderr, tallied, and cause a non-zero exit at the end —
  they are never silently swallowed.

### App 2 — `csvdiff`
Compares two `checksum` CSVs (a *baseline* and a *current*) and emits only the
files that differ.

- **Config:** `--baseline`/`BASELINE_CSV`, `--current`/`CURRENT_CSV`, `--output`/`OUTPUT`.
- **Matching key:** absolute path. Statuses: `MODIFIED` (sha differs),
  `ADDED` (only in current — often the interesting one), `DELETED` (only in baseline).
  Unchanged files are omitted.
- Optional `--fail-on-diff` to exit non-zero when any difference is found (for pipelines/alerting).

## Shared CSV schema (the contract — do not change casually)

**`checksum` output** (`internal/csvschema`):
```
absolute_path,filename,last_modified,size_bytes,sha256
```
- `last_modified`: RFC3339 UTC, e.g. `2026-07-07T03:10:00Z`
- `size_bytes`: integer bytes
- `sha256`: lowercase hex (column name tracks the chosen algo)

**`csvdiff` output**:
```
status,absolute_path,filename,baseline_sha,current_sha,baseline_size,current_size,baseline_modified,current_modified
```

Any change to these headers must be made in `internal/csvschema` and reflected in
both apps and the golden testdata.

## Build / test / run

```bash
# Build & test locally (Go 1.23+)
go build ./...
go test ./...
go vet ./...

# Run App 1 locally
go run ./cmd/checksum --root ./testdata/tree --ext .php --ext .js --output /tmp/current.csv

# Run App 2 locally
go run ./cmd/csvdiff --baseline baseline.csv --current current.csv --output changes.csv

# Docker (source disk mounted READ-ONLY; output dir writable)
docker build -f docker/checksum.Dockerfile -t fsd-checksum .
docker run --rm \
  -v /path/to/disk:/mnt/data:ro \
  -v "$PWD/out":/out \
  -e EXTENSIONS=".php,.js,.phtml" -e OUTPUT=/out/current.csv \
  fsd-checksum
```

## Conventions & invariants (keep these true)

- **Determinism:** `checksum` output is always sorted by absolute path. Two runs
  over identical trees must produce byte-identical CSVs. Tests rely on this.
- **Source data is read-only:** never write to or modify the tree being scanned.
  Docker mounts use `:ro`. Containers run as a non-root user.
- **Surface every read error** — matches the core purpose (a tamper check that
  silently skips unreadable files is dangerous). Exit codes carry meaning:
  `0` clean, `1` fatal/config error, `2` completed but some files failed to read
  (App 1) / bad input CSV (App 2). See `PLAN.md` for the full table.
- **Don't follow symlinks** by default (avoids hashing files outside the scanned
  tree and TOCTOU games); symlinks are reported as skips.
- **`internal/csvschema` is the single source of truth** for CSV structure — both
  apps import it; never hand-roll headers in `cmd/`.
- Standard library only where reasonable (`crypto/sha256`, `encoding/csv`,
  `path/filepath`); add third-party deps only with a note in `PLAN.md`.
- Keep the two binaries independent — the only thing they share is `internal/csvschema`.

## Status

Greenfield. Nothing is implemented yet — start from `PLAN.md`, which holds the
locked decisions and the phased task breakdown.
