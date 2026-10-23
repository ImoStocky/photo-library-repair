# Photo library repair

`plr` is a Go CLI that builds a photo library from directories: ingest
metadata into SQLite, group near-duplicates, resolve which pixels and
EXIF to keep, then **amalgamate** (CLI: `apply`) chosen images into a
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
plr group [--max-hamming 6] [--db plr.db]
plr plan  [--db plr.db]
plr apply --target RESULT [--dry-run] [--force-conflicts] [--db plr.db]

# one-shot (scan → group → plan → apply/amalgamate)
plr build --dir PHOTO --target RESULT
```

| Command | Role |
| --- | --- |
| `scan` | Walk dirs; record sha256, 16-byte perceptual hash, EXIF into SQLite. Skips already-scanned dirs unless `--force`. |
| `group` | Exact sha256 + Hamming-near phash → groups |
| `plan` | Per group: best pixels, earliest capture, GPS; mark conflicts |
| `apply` | **Amalgamate:** copy planned files to `--target`, write merged EXIF |
| `build` | Full pipeline in one go |
| `report` | Summarize library / groups / plans |
| `selftest` | Built-in parity checks |

**Naming:** *amalgamate* is the product verb for materializing the plan;
the CLI command is still `apply`. See
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

Gaps and bypasses: [`docs/mvp-gaps.md`](docs/mvp-gaps.md).

## Design notes for agents

Chapter index: [`.manual/INDEX.md`](.manual/INDEX.md).  
Agent rules: [`AGENTS.md`](AGENTS.md).

The archived client/server draft is **not** the current target:
[`.manual/_archive/go-design-draft.md`](.manual/_archive/go-design-draft.md).
