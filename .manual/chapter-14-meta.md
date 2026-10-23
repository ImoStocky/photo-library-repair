# Chapter 14: Meta (S4)

**Read when:** changing EXIF read/write, capture/offset/GPS parsing, or
ExifTool integration.

**Spec pieces:** capture / GPS / camera / other-tags in
[chapter 02 §2.3](chapter-02-semantics.md).
**Decision:** [chapter 05 §9.3](chapter-05-decisions.md).
**Status:** Deliberate MVP diverge (pure-Go, limited tags).

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
| `internal/meta` | JPEG APP1 TIFF parse → capture wall clock, offset, GPS, orientation |
| `internal/exifwrite` | JPEG APP1 rewrite of capture, offset, GPS, orientation |
| `internal/fingerprint` | Calls `meta.FromJPEG` during scan |

**Gaps vs design:** no ExifTool; no HEIC/TIFF/PNG metadata write path beyond
what apply skips; no copy of description/artist/copyright onto output;
camera-field richness for “is camera file?” / “most camera fields” scoring
is incomplete relative to §2.3.

## Capture parsing rules (shared with S5)

- Only `DateTimeOriginal` is capture time.
- Offsets: `OffsetTimeOriginal`, or embedded `+10:00` / `+1000` / `Z`.
- Mixed zoned + bare → compare wall clocks when spanning.
- `(0,0)` GPS ignored.

Implement parsing here; **policy** (which time wins, conflicts) lives in
[chapter 15](chapter-15-aggregate.md).

## Concurrency

Design: one ExifTool process. MVP: no shared process; read during scan
workers, write during apply per file.
