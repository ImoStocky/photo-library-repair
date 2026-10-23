# Chapter 12: Hash (S2)

**Read when:** changing phash, Hamming, orientation, or candidate banding.

**Spec:** [chapter 02 §2.1](chapter-02-semantics.md).
**Decision:** [chapter 05 §9.1](chapter-05-decisions.md).
**Status:** Mostly done (no banding).

## Responsibility

Orient + DCT phash (16-byte), Hamming distance, banding candidate index.
Pure.

## Spec summary

- 16-byte DCT phash after EXIF orientation (`hash_size=16`).
- JPEG may use ~64 px edge preview for speed; store full oriented dims.
- Default radius for clustering/merge: **12** bits (MVP CLI default is 12).
- Cache key `(path, size, mtime_ns)`.

## Target package

`internal/hash` — `Phash`, `Hamming`, `Candidates` (banding).

Banding: if two hashes differ in ≤ k bits, split into `(k+1)` bands so they
share at least one band (pigeonhole) — candidate generation without all
pairs.

## Current package

- `internal/phash` — `Compute`, `Hash.Hamming`, Lanczos resize, DCT.
- `internal/fingerprint` — decode by kind, apply orientation, call phash,
  attach EXIF facts from `meta`.

**Gaps:** no `Candidates` / band index; no durable phash cache table
(re-hash on forced re-scan).

## Pseudocode (target)

```go
func Phash(img image.Image) Hash {
    g := toGray(img)               // ITU-R 601-2 luma
    r := resizeLanczos(g, 32, 16)  // twice the 16x8 hash region
    d := dct2(r)
    low := /* top-left 16x8 low-frequency quadrant */
    med := median(low)
    // bit i set iff low[i] >= med  -> 128 bits = 16 bytes
}

func Hamming(a, b Hash) int { /* popcount(a xor b) */ }

func Candidates(hashes []Hash, k int) map[int64][]int {
    bands := k + 1
    // index indices by band key; pairs sharing a band are candidates
}
```

## Tests to preserve

Orientation invariance, Hamming symmetry, (when added) banding completeness.
