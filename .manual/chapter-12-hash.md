# Chapter 12: Hash (S2)

**Read when:** changing phash, Hamming, orientation, or candidate banding.

**Spec:** [chapter 02 §2.1](chapter-02-semantics.md).
**Decision:** [chapter 05 §9.1](chapter-05-decisions.md).
**Status:** Mostly done (banding in `group.Aggregate`; multi-alg/size phash).

## Responsibility

Orient + perceptual hash, Hamming distance, banding candidate index. Pure.

## Spec summary

- Default: legacy 16×8 DCT phash (128 bits) after EXIF orientation.
- Also via `--phash-alg` / `--phash-size`: square DCT **8×8 / 16×16 / 32×32**,
  and Czkawka/image_hasher **mean**, **median**, **gradient**,
  **vert-gradient**, **double-gradient**, **blockhash** (sizes 8/16/32).
- Default radius for clustering/merge: **12** bits (MVP CLI default is 12).
- Cache key `(path, size, mtime_ns)`.

## Target package

`internal/hash` — `Phash`, `Hamming`, `Candidates` (banding).

Banding: if two hashes differ in ≤ k bits, split into `(k+1)` bands so they
share at least one band (pigeonhole) — candidate generation without all
pairs.

## Current package

- `internal/phash` — `Compute` / `ComputeWith(Config)`, `Hash.Hamming`,
  `Hash.Band`, Lanczos resize, DCT (legacy + square sizes), and Czkawka
  Mean / Median / Gradient / VertGradient / DoubleGradient / Blockhash.
- `internal/fingerprint` — decode by kind, apply orientation, call phash
  (`Hasher(cfg, tool)`), attach EXIF facts from `meta`.
- `internal/group` — `Aggregate` runs the band candidate index over the
  stored hash bit length (`maxHamming+1` bands, width capped at 64 bits);
  two hashes at distance ≤ `maxHamming` must share a whole band
  (pigeonhole), so only bucket mates are exact-tested. Identical sha256 /
  phash strings group through O(n) maps.
- `internal/ingest` — fingerprint cache: on a forced re-scan, a file whose
  (size, mtime) matches the stored `images` row keeps its stored sha256,
  phash and EXIF facts instead of being re-hashed.

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
