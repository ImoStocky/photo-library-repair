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
  use this as refference to know how to use kronk sdk for ai integrations. 

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

## graphify

This project has a knowledge graph at graphify-out/ with god nodes, community structure, and cross-file relationships.

When the user types `/graphify`, use the installed graphify skill or instructions before doing anything else.

Rules:
- When graphify-out/graph.json exists, run `graphify query "<question>"` before your first grep, search or source-file read for a codebase question, and put this rule in every subagent prompt that explores code. Read raw files only for lines the graph or an earlier read already located. Use `graphify path "<A>" "<B>"` for relationships and `graphify explain "<concept>"` for focused concepts. These return a scoped subgraph, usually much smaller than GRAPH_REPORT.md or raw grep output.
- Dirty graphify-out/ files are expected after hooks or incremental updates; dirty graph files are not a reason to skip graphify. Only skip graphify if the task is about stale or incorrect graph output, or the user explicitly says not to use it.
- If graphify-out/wiki/index.md exists, use it for broad navigation instead of raw source browsing.
- Read graphify-out/GRAPH_REPORT.md only for broad architecture review or when query/path/explain do not surface enough context.
- After modifying code, run `graphify update .` to keep the graph current (AST-only, no API cost).
- A committed graphify-out/graph.json is rewritten whole on every build, so a large diff to it in a pull request is expected. Do not flag it as churn or suggest skipping the graph rebuild or its hooks.
