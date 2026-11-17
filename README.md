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
| `filter` | Walk a materialized library; apply YAML firewall rules; move drops to `--denied-dir`. |
| `group` | Exact sha256 + Hamming-near phash → groups |
| `plan` | Per group: best pixels, earliest capture, GPS; mark conflicts |
| `apply` | **Materialize:** copy (or `--move`) planned files to `--target`, write merged EXIF and `report.json` |
| `build` | Full pipeline in one go |
| `increment` | Grow an existing library: scan new dirs into the same DB, then re-group / re-plan / re-apply |
| `report` | Summarize library / groups / plans |
| `selftest` | Built-in parity checks |

## Filtering

Optional firewall-style rules act on the **materialized library** (`--target`),
not on source scans. Omit `--filter-rules` and behaviour is unchanged.

```text
plr apply --target LIB --filter-rules rules.yaml [--denied-dir LIB/denied] [--dry-run]
plr filter --dir LIB --filter-rules rules.yaml [--db plr.db] [--denied-dir LIB/denied] [--dry-run]
plr filter --filter-rules rules.yaml --explain LIB/suspect.jpg
```

`apply --filter-rules` skips writing drops and moves an existing target file
into denied (and clears that group's apply-ledger row). `plr filter` walks an
already-materialized library the same way and then clears matching ledger
rows in `--db` so a later apply does not treat the move as still present.
Conflict groups still materialize; filter may move them like any other file.
`build` / `increment` pass `--filter-rules` through to the apply phase only.

Example `rules.yaml`:

```yaml
version: 1
default: accept

rules:
  - name: abnormal-aspect-ratio
    match: { min_aspect_ratio: 16 }
    action: drop

  - name: miniature
    match: { format: jpeg, aspect_ratio: 1, max_width: 499, max_height: 499 }
    action: drop

  - name: clutter
    match:
      any:
        - all: [ { format: png }, { max_file_size: 50KB } ]
        - all: [ { max_width: 127 }, { max_height: 127 } ]
    action: drop
```

Dropped files are **moved** to `<denied-dir>/<rule-name>/<relative-path>` (default
`<target-or-dir>/denied`). Default drops use `_default/`. Moves are logged in
`denied/manifest.jsonl`. The denied directory is skipped on later `plr filter` walks.

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
