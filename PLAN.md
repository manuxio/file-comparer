# PLAN.md — file-comparer

Implementation plan for the two-app forensic tamper-detection toolkit.
Read `CLAUDE.md` first for the high-level overview; this file holds the locked
decisions, detailed specs, and the phased task breakdown.

## 1. Goal & context

During (or after) a suspected compromise we need to quickly answer: **which files
changed relative to the last known-good backup?** Hashing every file and diffing
the hash manifests is far cheaper than diffing content, and it narrows a full
content review down to just the files that actually differ.

- **App 1 `checksum`** produces a hash manifest (CSV) of a directory tree.
- **App 2 `csvdiff`** diffs two manifests (baseline vs current) and lists the
  added / deleted / modified files.

Both ship as small Docker images and are run against mounted disks.

## 2. Locked decisions

| Decision | Choice | Rationale |
|---|---|---|
| Language / runtime | **Go 1.23+** | Single static binary, tiny distroless image, fast concurrent hashing, no runtime deps. |
| Hash algorithm | **SHA-256** default (`--algo`, allow `sha512`) | Collision-resistant for tamper detection; MD5/SHA-1 explicitly *not* offered for security use. |
| Output format | CSV via `encoding/csv` | Simple, diffable, spreadsheet-friendly, matches the requirement. |
| Manifest ordering | **Sorted by absolute path** | Deterministic, reproducible, git/diff-friendly, enables golden tests. |
| Source access | **Read-only** mounts (`:ro`); container runs **non-root** | Never mutate the disk under investigation. |
| Symlinks | **Not followed** by default; reported as skips | Prevents hashing outside the tree + TOCTOU. |
| CSV contract | Centralized in `internal/csvschema` | One source of truth shared by both apps. |
| Base image | `golang:1.23` builder → `gcr.io/distroless/static-debian12:nonroot` | Minimal attack surface, no shell, non-root by default. |
| Module path | `github.com/iqera/file_sha_diff` (adjust to actual repo) | Repo will be published as `file-comparer`. |
| Repo | Public GitHub repo **`file-comparer`**, default branch `main` | Per request. |

## 3. Repository layout

```
cmd/checksum/main.go        # flag+env parsing, wiring, exit codes
cmd/csvdiff/main.go
internal/csvschema/         # headers, record structs, parse/format helpers, algo naming
internal/scan/              # walk + extension filter + hashing + sorted CSV writer
internal/compare/           # load 2 CSVs, index by path, diff, emit changes CSV
docker/checksum.Dockerfile
docker/csvdiff.Dockerfile
docker-compose.yml
testdata/                   # sample trees + golden CSVs
```

## 4. App 1 — `checksum` (detailed spec)

### 4.1 Configuration (flag OR env; flag takes precedence)
| Flag | Env | Default | Meaning |
|---|---|---|---|
| `--root` | `SCAN_ROOT` | `/mnt/data` | Directory tree to scan. Must exist & be a dir. |
| `--ext` (repeatable) | `EXTENSIONS` (comma-sep) | *(required)* | Extensions to include, matched **case-insensitively**. Normalize to leading dot (`php` → `.php`). |
| `--output` | `OUTPUT` | *(required)* | Output CSV path (inside container → mounted volume). Parent dir must be writable. |
| `--algo` | `ALGO` | `sha256` | `sha256` or `sha512`. |
| `--follow-symlinks` | `FOLLOW_SYMLINKS` | `false` | Opt-in only. |
| `--fail-fast` | `FAIL_FAST` | `false` | Abort on first read error instead of tallying. |

### 4.2 Algorithm
1. Validate config; if invalid, print a clear message to stderr and **exit 1**.
2. `filepath.WalkDir(root)`; for each regular file whose lowercased extension is
   in the allow-list, hand it to a bounded worker pool for hashing.
3. Each worker streams the file through the hash (constant memory) and records:
   `absolute_path`, `filename` (base name incl. extension), `last_modified`
   (RFC3339 UTC), `size_bytes`, `sha` (lowercase hex).
4. Collect records, **sort by absolute path**, write CSV with header.
5. Print a summary to stderr: files scanned, matched, hashed, errored, elapsed.

> **Scaling note:** buffering all records to sort is fine for typical web-root
> sizes (10⁴–10⁵ files). If we ever need 10⁷+ files, switch to a streaming
> writer + external sort. Documented, not built yet.

### 4.3 Error handling (core requirement)
- **Config / setup errors** (missing root, no extensions, unwritable output,
  unknown algo) → single clear stderr message, **exit 1**, nothing written.
- **Per-file errors** (permission denied, I/O error, vanished mid-scan) → logged
  to stderr immediately with the path and cause, counted. They are **never
  silently skipped**. After the walk, if `errorCount > 0`, print a summary and
  **exit 2** (partial success). `--fail-fast` turns the first one into exit 2
  immediately.
- Symlinks & non-regular files (devices, sockets) are reported as skipped with a
  reason, not hashed.

### 4.4 Exit codes
| Code | Meaning |
|---|---|
| 0 | Completed; every matched file hashed successfully. |
| 1 | Fatal config/setup error; no manifest produced. |
| 2 | Manifest produced, but ≥1 file could not be read. |

## 5. App 2 — `csvdiff` (detailed spec)

### 5.1 Configuration
| Flag | Env | Meaning |
|---|---|---|
| `--baseline` | `BASELINE_CSV` | Manifest of the last good backup. |
| `--current` | `CURRENT_CSV` | Manifest of the current/suspect system. |
| `--output` | `OUTPUT` | Changes CSV. |
| `--strip-baseline-prefix` / `--strip-current-prefix` | — | Optional path-prefix normalization so manifests taken at different mount points compare correctly (e.g. `/mnt/backup` vs `/mnt/data`). |
| `--fail-on-diff` | `FAIL_ON_DIFF` | Exit non-zero when any difference is found. |

### 5.2 Algorithm
1. Load & validate both CSVs against `internal/csvschema` (header check, parse
   rows into records, index by the — optionally prefix-stripped — absolute path).
   Reject on malformed rows or duplicate keys with a clear message.
2. For the union of paths:
   - in both, `sha` differs → **`MODIFIED`**
   - in current only → **`ADDED`** (new file — frequently the smoking gun)
   - in baseline only → **`DELETED`**
   - in both, `sha` equal → omitted (unchanged)
3. Write the changes CSV **sorted by (status, absolute_path)**; print a summary
   (modified / added / deleted counts) to stderr.

### 5.3 Exit codes
| Code | Meaning |
|---|---|
| 0 | Comparison succeeded (0 unless `--fail-on-diff` and diffs exist). |
| 1 | Fatal error (missing/unreadable input, bad flags). |
| 2 | Input CSV failed schema validation. |
| 3 | Success **and** differences found — only when `--fail-on-diff` is set. |

## 6. Docker

- **Multi-stage:** `golang:1.23` build stage (`CGO_ENABLED=0 go build`) →
  `gcr.io/distroless/static-debian12:nonroot` final stage with just the binary.
- **`checksum`** expects `/mnt/data` (`:ro`) and an output dir (e.g. `/out`, rw).
- **`csvdiff`** expects an input dir with both CSVs and an output dir.
- `docker-compose.yml` wires volumes/env for a two-step run (baseline optional,
  usually produced separately against the backup).
- Images run as non-root; no shell in the final image.

## 7. Testing strategy

- `internal/scan`: table tests over `testdata/tree` — extension filtering
  (case-insensitivity), sorted/deterministic output vs a golden CSV, symlink
  skipping, and a deliberately unreadable file → asserts exit 2 + stderr message.
- `internal/compare`: golden baseline+current pairs covering MODIFIED/ADDED/
  DELETED/unchanged, prefix stripping, duplicate-key rejection, malformed input.
- `internal/csvschema`: round-trip parse/format; header mismatch detection.
- CLI smoke tests via `go test` invoking the binaries on `testdata/`.
- Determinism guard: run `checksum` twice, assert byte-identical output.

## 8. Milestones / task breakdown

- [ ] **M0 — Scaffold:** `go.mod`, package skeletons, this plan checked in, CI stub.
- [ ] **M1 — csvschema:** headers, record structs, parse/format, algo registry, tests.
- [ ] **M2 — App 1 core (`internal/scan`):** walk + ext filter + streaming hash +
      sorted writer; error tallying; unit tests + golden CSV.
- [ ] **M3 — App 1 CLI (`cmd/checksum`):** flag/env parsing, exit codes, summary.
- [ ] **M4 — App 2 core (`internal/compare`):** load/index/diff + prefix strip; tests.
- [ ] **M5 — App 2 CLI (`cmd/csvdiff`):** flags/env, exit codes, `--fail-on-diff`.
- [ ] **M6 — Docker:** both Dockerfiles + docker-compose; verify read-only mount
      and non-root run end-to-end.
- [ ] **M7 — Docs & polish:** README with real IR walkthrough, example CSVs, CI
      (build + test + vet) on push.

## 9. Open questions / future work

- **Path normalization:** baseline and current manifests are often captured at
  different mount points. `--strip-*-prefix` covers the simple case; do we need
  regex/relative-path normalization? (Deferred until a real mismatch shows up.)
- **Metadata-only changes:** currently a file with identical SHA but changed
  mtime/size is treated as unchanged (SHA is authoritative). Confirm that's the
  desired behavior for the IR use case.
- **Very large trees:** streaming + external sort if we exceed ~10⁷ files.
- **Signing the manifest:** optionally sign `baseline.csv` so the baseline itself
  can be trusted as tamper-evident. (Nice-to-have.)
