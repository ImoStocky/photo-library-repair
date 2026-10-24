# Third-party dependency decisions

Image-format libraries for `plr`. The phash engine itself stays
dependency-free (pure-Go LANCZOS3 + DCT, [chapter 12](../.manual/chapter-12-hash.md));
the choices below are for **metadata (EXIF)** and **HEIC decode**.

## Current state

- EXIF read/write is the real **ExifTool 13.59**, embedded as
  WebAssembly (pure Go, no cgo, no system dependencies): `internal/exif`
  wraps `yashikota/exiftool-go`. Every available tag (capture time,
  offset, GPS, orientation, and title/description/artist/copyright/camera
  fields, …) is read; apply writes the merged facts and **all other tags
  in the source survive the write**.
- The hand-rolled JPEG-only reader (`internal/meta`) and writer
  (`internal/exifwrite`) stay in the tree as the **fallback** path: if the
  engine fails to start, `plr` warns and continues with the minimal
  tags-only read/write.
- HEIC is recognized by kind sniff and recorded, but has **no decoder**:
  `internal/fingerprint` yields an empty phash for HEIC files, so they cannot
  be clustered or plan-resolved by pixels today.

## Decisions

| Concern | Library | Why |
| --- | --- | --- |
| EXIF read + write | **`yashikota/exiftool-go` v1.2.0** (pure Go, no cgo) | Embeds the real ExifTool 13.59 as WebAssembly (zeroperl on wazero): full tag parity (EXIF, IPTC, XMP, maker notes), read *and* write, single binary, zero system dependencies. One shared instance is safe across worker goroutines (internal mutex). |
| HEIC decode | **`vegidio/heif-go`** (pure Go) | Only fully native option: HEIF/HEIC encode/decode with no CGO and no system libraries. Keeps the build trivial, consistent with the dependency-free phash engine. |

**ExifTool write details:**

- All pipeline writes go to a **new file** (apply pattern: write `dest.tmp`,
  rename into place). In-place engine writes (`WriteMetadata(src, "", …)`)
  are **unsafe**: GPS seconds get rescaled (measured 13.30" → 0.22", i.e.
  divided by 60). Non-in-place writes round-trip DMS exactly.
- No DB schema change: the full EXIF is **not** stored in SQLite — the
  source file is the source of truth; apply reads it directly when writing
  the copy.

**Revisit triggers:**

- EXIF: WASM cost profile. One-time engine init costs ~9s per process;
  per-file cost is negligible (measured ~0.02s/file on 640×480 JPEGs,
  dominated by init). If throughput at large-library scale is a problem,
  switch to a native exiftool (there are no maintained libexiftool C-API
  bindings; an exec wrapper with `-stay_open` would be the shape of it).
- HEIC: first pick, not final. Re-evaluate once HEIC scan/decode
  performance is measured on a real library — the pure-Go decode cost per
  file in the scan phase is the open question (every HEIC is fully decoded
  for its phash).

## Considered, deferred

| Area | Library | CGO | Why not (yet) |
| --- | --- | --- | --- |
| EXIF | `dsoprea/go-exif` | no | De-facto standard pure-Go EXIF reader/writer, but limited to the TIFF/EXIF payload: no IPTC/XMP/maker-notes parity, i.e. no single full-data engine for "read and write all available data". |
| EXIF | `ncruces/go-exiftool` | no | Thin wrapper that ships and execs a self-contained ExifTool distro — full parity, but a second binary to ship and manage; nothing to embed. |
| EXIF | `barasher/go-exiftool`, `mostlygeek/go-exiftool` | no | Exec wrappers around a **system** exiftool — pushes the dependency onto the user's machine (not installed on this one). |
| EXIF | `rwcarlsen/goexif` | no | The classic, most widely used Go EXIF library — but **read-only** and effectively unmaintained (the ecosystem keeps circling back to it; no writer to cover `exifwrite`). |
| EXIF | `mschilli/go-exif`, `imclaren/go-exif`, `krak3n/go-exif` | no | Forks of the dsoprea line; no reason to prefer a fork. |
| EXIF | `mostlygeek/goexif` | no | Spec-based pure-Go decode, read-only; the same limited-tags story as the hand-rolled reader. |
| EXIF | libexif CGO bindings (`martinheidegger/exif`, `xiam/exif`, …) | yes | Basic tag set through libexif, CGO, unmaintained; and no maintained libexiftool C-API bindings exist. |
| HEIC | `strukturag/libheif` Go bindings | yes | Battle-tested (libde265) but CGO + build-toolchain burden; the fallback if `vegidio/heif-go` proves too slow or format-limited. |
| HEIC | `klippa-app/go-libheif` | yes | `go get`-table libheif wrapper (HEIC/HEIF/AVIF); same CGO trade-off, less proven than the upstream bindings. |
| HEIC | `xo/heif` | yes | Go wrapper around libheif, encode + decode for any libheif-supported format; same CGO trade-off. |
| HEIC | `f0reth/go-heic` | no (purego) | CGO-free, but loads `libheif`/`libde265` shared libraries at runtime — pushes the dependency onto the user's machine. |
| HEIC | `gen2brain/heic` | no (wazero) | CGO-free: Rust heic decoder compiled to WASM, run under wazero; tries a local `libheif` via purego first, falls back to the embedded WASM. Interesting middle ground, but the WASM decode is the slow path and the library is young. |
| HEIC | `jdeng/goheif` (+ forks `rpajarola/goheif`, `adrium/goheif`) | yes | libde265-based; older, less maintained. |
| HEIC | `go4.org/media/heif` | no | Container/metadata only — no pixel decode, useless for phash. |

Related: [chapter 14 — Meta](../.manual/chapter-14-meta.md) (EXIF read/write
responsibilities), [chapter 05 §9.1](../.manual/chapter-05-decisions.md)
(phash engine stays cgo-free).
