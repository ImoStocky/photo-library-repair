# AGENTS.md

Guidance for coding agents working in **photo-library-repair** (`plr`).

## Product heading

Local Go CLI: scan → group → plan → apply/amalgamate, SQLite library,
incremental re-scan. Read [`.manual/chapter-01-goals-and-status.md`](.manual/chapter-01-goals-and-status.md)
first. Chapter index: [`.manual/INDEX.md`](.manual/INDEX.md).

- **apply ≡ amalgamate** — same step; CLI stays `plr apply` until an
  explicit rename. Do not rename while other agents may be mid-flight.
- The archived client/server draft
  ([`.manual/_archive/go-design-draft.md`](.manual/_archive/go-design-draft.md))
  is **not** the current target.
- [`.manual/chapter-20-developer-guide.md`](.manual/chapter-20-developer-guide.md)
  is Kronk SDK notes, not this repo’s architecture.

## Basic rules

After modifying any `.go` file:

1. `gofmt -s -w` on the changed files
2. `go vet` on the changed package
3. `staticcheck` and `go fix` on the changed package
4. Prefer focused tests: `go test ./internal/<pkg>/...`

## Layout

```text
main.go                 # plr entry
internal/cli/           # commands
internal/ingest/        # scan
internal/phash/         # 16-byte DCT perceptual hash
internal/fingerprint/   # decode + hash + EXIF facts
internal/group/         # Aggregate + ResolveGroup (plan/conflicts)
internal/meta/          # JPEG EXIF read (fallback)
internal/exif/          # full-data EXIF engine (embedded ExifTool, WASM)
internal/exifwrite/     # JPEG EXIF write (fallback)
internal/store/         # SQLite sources/images/groups
internal/apply/         # amalgamate (copy + write EXIF)
internal/selftest/      # plr selftest
```

## Do not

- Reintroduce the Python prototype or treat README Python history as current
- Wire gRPC / commit-log / ExifTool as required for the MVP heading
- Rename `apply` → `amalgamate` without an explicit user request
