# Third-party dependency decisions

Image-format libraries for `plr`. The phash engine itself stays
dependency-free (pure-Go LANCZOS3 + DCT, [chapter 12](../.manual/chapter-12-hash.md));
the choices below are for **metadata (EXIF)** and **HEIC decode**.

## Current state

- EXIF read/write is hand-rolled, JPEG-only: `internal/meta` (read) and
  `internal/exifwrite` (write) parse/emit JPEG APP1/TIFF directly. Covers the
  tags the pipeline needs: capture time, offset, GPS, orientation
  ([mvp-gaps #6](mvp-gaps.md)).
- HEIC is recognized by kind sniff and recorded, but has **no decoder**:
  `internal/fingerprint` yields an empty phash for HEIC files, so they cannot
  be clustered or plan-resolved by pixels today.

## Decisions

| Concern | Library | Why |
| --- | --- | --- |
| EXIF read + write | **`dsoprea/go-exif`** (pure Go) | De-facto standard: complete, heavily tested, standards-driven. Parse, update, or build EXIF blocks; IFD/tag visitor API. Works on any container that embeds a TIFF payload — including the HEIC `Exif` box — so it serves both the current JPEG path and the future HEIC path. |
| HEIC decode | **`vegidio/heif-go`** (pure Go) | Only fully native option: HEIF/HEIC encode/decode with no CGO and no system libraries. Keeps the build trivial, consistent with the dependency-free phash engine. |

**Revisit trigger:** these are first picks, not final ones. Re-evaluate once
HEIC scan/decode performance is measured on a real library — the pure-Go
decode cost per file in the scan phase is the open question (every HEIC is
fully decoded for its phash).

## Considered, deferred

| Area | Library | CGO | Why not (yet) |
| --- | --- | --- | --- |
| EXIF | `rwcarlsen/goexif` | no | The classic, most widely used Go EXIF library — but **read-only** and effectively unmaintained (the ecosystem keeps circling back to it; no writer to cover `exifwrite`). |
| EXIF | `mschilli/go-exif`, `imclaren/go-exif`, `krak3n/go-exif` | no | Forks of the dsoprea line; no reason to prefer a fork. |
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
