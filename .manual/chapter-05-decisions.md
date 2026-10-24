# Chapter 05: Key technical decisions

## 9.1 Perceptual hash in Go

Prototype: `imagehash.phash` (gray → 64×64 LANCZOS → 2D DCT → 16×16
low-freq → median threshold, 256 bits) — history only.

**Decision:** follow the czkawka / img_hash pipeline in Go
(`internal/phash` today; design name `internal/hash`): gray → 32×16
LANCZOS3 → 2D DCT → top-left 16×8 low-frequency quadrant → median
threshold = **16 bytes (128 bits)**. czkawka offers 8/16/32/64-byte
hash sizes; 16 gave the best results on our data, so it is fixed.
Not bit-identical to `imagehash` — acceptable because Go re-hashes the
seed and only compares Go hashes to Go hashes.

*Open:* bit-identical DCT/resize only if we must reuse a Python `phash.json`
cache.

**MVP:** implemented in `internal/phash` (+ Lanczos/DCT helpers).

## 9.2 Clustering engine

**Decision:** drop HDBSCAN/OPTICS; use connected components under
“Hamming ≤ `max_hamming`” with banding. Same relation as the prototype
post-pass and incremental matcher; deterministic; unifies seed + merge.

*Trade-off:* can bridge near-copy chains HDBSCAN might leave as noise —
consistent with prototype incremental behavior.

*Open:* optional density refinement later if real data over-merges. Prefer
**gonum** if vector clustering is ever needed; keep phash engine cgo-free.

**MVP:** CC in `group.Aggregate`; no banding yet (O(n²)).

## 9.3 Metadata engine

**Decision (design):** keep **ExifTool** `-stay_open`, mutex-guarded,
batched (~200 reads / ~40 writes). Pure-Go write risks tag-parity
regressions.

*Open:* pure-Go read for common case; writes stay on ExifTool.

**MVP:** `internal/exif` — the real ExifTool 13.59 embedded as WebAssembly
(pure Go, no cgo, no system dependencies). Reads every available tag;
writes go to a new file with all other tags preserved (in-place engine
writes are unsafe — see `docs/dependencies.md`). Built-in
`internal/meta` + `internal/exifwrite` remain the fallback when the engine
cannot start.

## 9.4 Storage

**Decision:** SQLite single file; store holds the **index**, not pixels.
*Open:* Postgres behind the same interface past ~10⁶ pictures / replication.

**MVP:** SQLite with simpler schema ([chapter 17](chapter-17-store.md)).

## 9.5 Transport

**Decision:** gRPC + SSE facade. *Open:* plain HTTP/JSON for first cut; API
surface is transport-agnostic.

**MVP:** no network API — local CLI only.

## 9.6 Concurrency

Single ExifTool process = only shared mutable I/O (mutex). Hashing
embarrassingly parallel (`≈ NumCPU()`, cap 32). Cluster/aggregate/merge:
single goroutine over in-memory slices.

**MVP:** worker pool in `ingest`; one embedded ExifTool engine instance
per CLI command (internal mutex), shared by the workers.

## 9.7 Format libraries (EXIF / HEIC)

**Decision:** EXIF read/write → `yashikota/exiftool-go` v1.2.0 (real
ExifTool 13.59 embedded as WebAssembly; pure Go, no cgo); HEIC decode →
`vegidio/heif-go` (pure Go). First picks, not final — re-evaluate once
HEIC decode performance is measured on a real library, and re-check the
WASM cost profile of the EXIF engine at large-library scale. Deferred
alternatives (exec wrappers, libexif CGO bindings, etc.) documented with
rationale.

**Full record:** [`docs/dependencies.md`](../docs/dependencies.md).
