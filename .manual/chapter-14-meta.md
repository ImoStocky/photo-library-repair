# Chapter 14: Meta (S4)

**Read when:** changing EXIF read/write, capture/offset/GPS parsing, or
ExifTool integration.

**Spec pieces:** capture / GPS / camera / other-tags in
[chapter 02 §2.3](chapter-02-semantics.md).
**Decision:** [chapter 05 §9.3](chapter-05-decisions.md).
**Status:** Done — full-data engine (embedded ExifTool 13.59, WebAssembly,
pure Go); built-in minimal reader/writer as fallback.

## Responsibility

Batch EXIF read (and write, for apply). Design: ExifTool I/O. Aggregation
rules stay pure and consume plain `Meta` maps.

## Target design

- Long-lived `exiftool -stay_open True -@ -`, mutex-guarded.
- Batch reads ~200 files; writes ~40.
- Background stderr drain for progress.
- Writes stay on ExifTool even if a pure-Go read path is added later.

Tags PlanGroup needs: `DateTimeOriginal`, `OffsetTimeOriginal`, GPS lat/lon,
camera field set (make/model/lens/…), and fill-in others (description,
artist, copyright, …).

## Current packages

| Package | Role |
| --- | --- |
| `internal/exif` | Full-data engine: embedded ExifTool 13.59 (WebAssembly, pure Go). `Facts` (pipeline facts from the full tag set), `Read` (every tag), `SetTags` / `WriteFacts` (merged write, all other tags preserved) |
| `internal/meta` | JPEG APP1 TIFF parse → capture wall clock, offset, GPS, orientation (fallback reader) |
| `internal/exifwrite` | JPEG APP1 rewrite of capture, offset, GPS, orientation (fallback writer) |
| `internal/fingerprint` | Scan-time facts via `fingerprint.WithEXIF` (engine), falling back to `meta.FromJPEG` |
| `internal/apply` | Merged write through the engine (`Options.EXIF`); falls back to `exifwrite` when the engine is nil |

**Gaps vs design:** the ExifTool integration is an embedded WASM instance,
not a spawned `-stay_open` process, so the batch read (~200) / write (~40)
shapes are not used — per-file calls under the engine's internal mutex are
fast enough at MVP scale. Writes never happen in place (the in-place engine
path rescales GPS seconds — see `docs/dependencies.md`). Camera-field
richness: the engine reads the fields, but the pipeline facts do not consume
camera make/model/lens for §2.3 "is camera file" / "most camera fields"
scoring yet.

## Capture parsing rules (shared with S5)

- Only `DateTimeOriginal` is capture time.
- Offsets: `OffsetTimeOriginal`, or embedded `+10:00` / `+1000` / `Z`.
- Mixed zoned + bare → compare wall clocks when spanning.
- `(0,0)` GPS ignored.

Implement parsing here; **policy** (which time wins, conflicts) lives in
[chapter 15](chapter-15-aggregate.md).

## Concurrency

Design: one ExifTool process. Current: one embedded engine instance per CLI
command (created by the CLI via `openEXIF`); the engine serializes calls
internally, so scan worker goroutines share a single instance. The full
EXIF is not stored in SQLite — the source file is the source of truth and
apply reads it directly.
