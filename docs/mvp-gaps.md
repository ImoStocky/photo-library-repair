# MVP gaps

What the `plr` MVP deliberately leaves out of the simpler product heading
([`.manual/` chapters](../.manual/INDEX.md); status in
[chapter 01](../.manual/chapter-01-goals-and-status.md)), and how to work
around it for now.

**Naming:** *amalgamate* = materialize the plan into `--target`. The CLI
command for that step is still `plr apply` (same meaning). Do not rename
while other agents are mid-flight. `plr build` is the one-shot full
pipeline (scan → group → plan → apply/amalgamate).

| # | Missing feature | Status | Bypass |
|---|-----------------|--------|--------|
| 1 | S6 partition in `increment` (present/matched/new actions, name-collision prefixes, ambiguity conflicts) | Implemented (core) | `plr increment` no longer re-aggregates the library: incoming images are grouped among themselves, then `group.Partition` classifies each member against the existing groups — present (sha or exact phash+size hit, joins the group it duplicates), joined (closest group within `--max-hamming`), new (own group). Library groups never merge through incoming images. Ties: smallest distance wins; equal distance to several groups joins the lowest id and prints an ambiguity warning. Action marking (`new`/`renamed`/`updated`/`untouched`, spec S7) is done at apply time by the apply ledger (row 14). Deferred: marking ambiguity as a plan conflict. Spec: [chapter 16](../.manual/chapter-16-merge.md). |
| 2 | Commit model: atomic snapshot, commit log, rollback | Implemented | Every plan state can be frozen: `plr commit [--message TEXT]` (run automatically by `build` and `increment` after the plan phase) snapshots the whole plan table in one transaction and prints the per-group diff (new/updated/removed) versus the previous commit. `plr rollback [--to N]` (default: the latest commit) restores an earlier snapshot and records the restore as a new commit, so the log stays append-only; image group assignments that point at groups the restore drops are zeroed. Scanned images are never touched. |
| 3 | `--move` for apply | Implemented | `--move` (apply, build, increment) deletes each source file once its copy with merged EXIF is fully in place. Duplicates inside a group and skipped conflict groups are left untouched; a failed source removal is reported (both files remain, data is safe). Dry-run prints "would move" and deletes nothing. Moving into the source directory renames the copy in place. |
| 4 | `report.json` written into the result directory | Implemented | Apply writes `report.json` into `--target`: the run summary (per-file source/dest, copied/moved/renamed/skipped counts, errors) plus every group's plan (status, reason, capture, GPS, pixel source). Dry-run writes nothing. |
| 5 | Client/server split (gRPC, job queue, SSE) | Not implemented | The CLI is a single binary; every phase reads and writes the same SQLite file. No bypass needed — this is the MVP form of the system. |
| 6 | ExifTool as the metadata engine | Implemented | `internal/exif` embeds the real ExifTool 13.59 (WebAssembly, pure Go, no cgo): reads every available tag, writes the merged facts, preserves all other tags. Built-in `meta`/`exifwrite` remain the fallback when the engine cannot start. Writes always go to a new file (in-place engine writes are unsafe — see [dependencies](dependencies.md)). |
| 7 | Banding candidate index for large libraries | Implemented | `group.Aggregate` no longer runs the all-pairs hamming pass: the 128-bit hash is split into `maxHamming+1` contiguous bands (`phash.Hash.Band`, width capped at 64 bits), and by the pigeonhole principle two hashes at distance ≤ `maxHamming` share a whole band. Only bucket mates are exact-tested, so grouping is near-linear; it stays exact for every radius, including exact (0) and negative (skipped). |
| 8 | phash cache keyed by (path, size, mtime) | Implemented | Forced re-scans compare each file's (size, mtime) with the stored row (new `mtime` column, unix nanoseconds): unchanged files keep their stored sha256/phash/EXIF facts and are never re-read, re-sha256'd or re-fingerprinted — `plr scan` counts them as `cached`. Changed files go through the normal fingerprint path. |
| 9 | HDBSCAN/OPTICS, `--min-cluster-size` | Intentionally omitted | Chain aggregation with `--max-hamming` is the clustering engine; tune the radius per library. |
| 10 | Copying extra camera tags (description, artist, copyright, ...) onto the output | Implemented | Engine writes preserve **every** metadata tag in the source (title, description, artist, copyright, camera fields, ...) while overwriting the merged facts; covered by `plr selftest` check 6. |
| 11 | `--conflict-hamming` flag | Implemented | Plan rule: a member pair whose perceptual hashes differ by 1…`--conflict-hamming` bits (default 2) and share a capture instant marks the group as a conflict ("near-identical pictures … with identical capture time"). Exact phash matches (distance 0 — normal re-encodes / duplicates) resolve via bestPixels. A negative limit disables the rule. Composes with the EXIF-disagreement conflicts, which take precedence; the AI resolver can settle the result. |
| 12 | Alternate perceptual-hash types (Czkawka algs + sizes) | Implemented | `internal/phash` / `--phash-alg` / `--phash-size` now cover: **dct** (legacy 16×8 default, or square 8/16/32), and Czkawka/image_hasher **mean**, **median**, **gradient**, **vert-gradient**, **double-gradient**, **blockhash** at sizes **8 / 16 / 32**. DoubleGradient packs horiz then vert comparisons on an (N/2+1)² Lanczos3 grid (40/144/544 bits). Against DedupTestSet, DCT alone still plateaus (~F1 0.875); remaining misses need the candidate/identity architecture below, not a larger radius. |
| 13 | Identity strategies: refine candidate clusters before the EXIF plan | Implemented (`vision` is a stub) | `group.Pipeline` chains Aggregate (DCT candidates) → `--identity` refine → plan. `pass` (default) keeps legacy grouping; `double-phash` unions members only when **both** secondary hashes agree (blockhash-16 ∩ double-gradient-16, radii `--identity-block-hamming` 24 / `--identity-grad-hamming` 20), with sha256 equality forcing a join; `vision` keeps the candidate intact until a real Kronk `VisionDecider` ships. Scan writes all three hashes per decode into `content_phash` keyed by **sha256** (identical bytes from different paths share one row); libraries scanned before this need `plr scan --force` once. |

## Notes

- The "file creation time precedes its own EXIF capture time" conflict is
  filesystem-dependent (birth time vs. mtime fallback), so the selftest suite
  exercises only the deterministic conflict kinds (offset disagreement, GPS
  disagreement).
- Selftest groups with a phash radius of 16 bits (the CLI default is 12) so
  quality re-encodings of the same picture always chain; the checks verify
  the resolution rules, not the default radius.
- DedupTestSet vs Czkawka (`results_similar_images*.json` under
  `Downloads/DedupTestSet/DedupTestSet/`): best DCT radius on that corpus is
  **14** (smallest radius on the max-F1 plateau); 20 is on the same plateau.

## Architecture direction (phash is unreliable alone)

Phash (any family) can both **miss** hard re-encodes/thumbnails and **merge**
unrelated frames. Implemented heading for grouping (Strategy + Chain):

1. **Scan is sha256-first** (done) — content digest keys `content_phash(sha256,
   alg, size, hex)`. Same bytes from different paths share one hash row.
   **Candidate phash stays DCT** (`images.phash`, default legacy 16×8);
   scan also writes blockhash-16 + double-gradient-16 into `content_phash`.
2. **Candidates** (done) — `Aggregate` on DCT phash (`--max-hamming`) proposes
   clusters (banding index).
3. **Identity strategies** (done; `vision` stub) — `--identity`: `pass`
   (legacy), `double-phash` (intersect blockhash∩double-gradient by sha256),
   `vision` (Kronk stub: keep candidate until a real VisionDecider returns
   Same).
4. **EXIF / conflict plan** — existing `ResolveGroup` (offsets, GPS,
   near-identical); keep conflicts in the library. Apply may still skip
   conflict pixels until `--force-conflicts`.
5. **Vision fallback** — Kronk pairwise identity later; conflict `--ai`
   resolver already exists.

Diameter-bounded clusters remain a follow-on hardening.
