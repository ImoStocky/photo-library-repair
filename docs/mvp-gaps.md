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
| 1 | S6 partition in `increment` (present/matched/new actions, name-collision prefixes, ambiguity conflicts) | Not implemented | `plr increment` (scan → group → plan → apply into the same DB) is the command form of the old bypass: new near-duplicates chain into existing groups through the same phash radius. The lossy part is exactly the missing S6 partition, specified in [chapter 16](../.manual/chapter-16-merge.md). |
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
| 12 | Alternate perceptual-hash types (Czkawka algs + sizes) | Implemented (hasher); architecture open | `internal/phash` / `--phash-alg` / `--phash-size` now cover: **dct** (legacy 16×8 default, or square 8/16/32), and Czkawka/image_hasher **mean**, **median**, **gradient**, **vert-gradient**, **double-gradient**, **blockhash** at sizes **8 / 16 / 32**. DoubleGradient packs horiz then vert comparisons on an (N/2+1)² Lanczos3 grid (40/144/544 bits). Against DedupTestSet, DCT alone still plateaus (~F1 0.875); remaining misses need the candidate/veto architecture below, not a larger radius. |

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
unrelated frames. Planned heading for grouping:

1. **Permissive candidates** — phash (and banding) only *propose* near-dup
   groups; default radius may stay generous for recall.
2. **EXIF veto** — if members' EXIF strongly disagrees (capture time/offset,
   camera, GPS, …), treat that as evidence they are **different photos** and
   refuse the merge (or mark a conflict). True duplicates usually share EXIF;
   a phash hit with mismatched EXIF is a false friend.
3. **Keep unresolvable conflicts in the library** — do not drop them from
   `groups` / apply forever; they stay recorded so a later pass can settle
   them. Apply may still skip materializing conflict pixels until forced.
4. **Vision fallback via Kronk** — for leftovers the rules cannot settle,
   call the Kronk SDK with a vision-capable model to decide whether images
   depict the same scene; write `resolved_by` accordingly (same pattern as
   today's `--ai` conflict resolver, broadened to "same picture?").

Diameter-bounded clusters (no silent friend-of-friend chains) remain a
companion hardening; see chapter 12 / 15 when implementing.
