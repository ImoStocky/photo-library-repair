# Photo library repair

`plr` is a Go CLI that builds a photo library from directories: ingest
metadata into SQLite, group near-duplicates, resolve which pixels and
EXIF to keep, then **materialize** (CLI: `apply`) chosen images into a
target folder. Sources stay on disk; the library lives in `plr.db`.

## Setup

Requires the Go version in `go.mod` (currently **1.27**; this machine has `go1.27.2`).

```text
go build -o plr .
./plr selftest
```

## Pipeline

```text
plr scan  --dir PHOTO [--dir MORE] [--force] [--db plr.db]
plr group [--max-hamming-chunking 20] [--db plr.db]
plr plan  [--ai] [--db plr.db]
plr apply --target RESULT [--dry-run] [--no-exif] [--move] [--db plr.db]

# one-shot (scan → group → plan → materialize)
plr build --dir PHOTO --target RESULT

# grow an existing library (scan new dirs into the same DB, then the same pipeline)
plr increment --dir NEW_PHOTOS --target RESULT --db plr.db
# ...and --force when new photos land in an already-registered directory
```

| Command | Role |
| --- | --- |
| `scan` | Walk dirs; record sha256, 16-byte perceptual hash, EXIF into SQLite. Skips already-scanned dirs unless `--force`. |
| `group` | Exact sha256 + Hamming-near phash → groups |
| `plan` | Per group: best pixels, earliest capture, GPS; mark conflicts |
| `apply` | **Materialize:** copy (or `--move`) planned files to `--target`, write merged EXIF and `report.json` |
| `build` | Full pipeline in one go |
| `increment` | Grow an existing library: scan new dirs into the same DB, then re-group / re-plan / re-apply |
| `report` | Summarize library / groups / plans |
| `selftest` | Built-in parity checks |

**EXIF engine:** read/write uses the real ExifTool 13.59 embedded as
WebAssembly (pure Go, no system dependencies); commands print
`exif engine: ExifTool 13.59 (embedded, full metadata)`. If the engine
cannot start, `plr` warns and falls back to the built-in minimal
JPEG reader/writer. Apply writes preserve every metadata tag in the
source. Details: [`docs/dependencies.md`](docs/dependencies.md).

**Naming:** *materialize* is the product verb for the `apply` step
(renamed from *amalgamate*); the CLI command stays `apply`. See
[`.manual/chapter-01-goals-and-status.md`](.manual/chapter-01-goals-and-status.md).

## Incremental growth

Scan additional directories into the same DB, then re-run `group`,
`plan`, and `apply`:

```text
plr scan --dir INBOX --db plr.db
plr group --db plr.db
plr plan --db plr.db
plr apply --target RESULT --db plr.db
```

Gaps and bypasses: [`docs/mvp-gaps.md`](docs/mvp-gaps.md). Every tunable
and its default: [`docs/defaults.md`](docs/defaults.md).

Eval helper: [`scripts/compare_groups.py`](scripts/compare_groups.py)
compares a `plr.db` group partition against Czkawka
`results_similar_images*.json` files (exact component match).

## Design notes for agents

Chapter index: [`.manual/INDEX.md`](.manual/INDEX.md).  
Agent rules: [`AGENTS.md`](AGENTS.md).

The archived client/server draft is **not** the current target:
[`.manual/_archive/go-design-draft.md`](.manual/_archive/go-design-draft.md).
