# MVP gaps

Defaults for every flag/constant mentioned here: [`defaults.md`](defaults.md).

What the `plr` MVP deliberately leaves out of the simpler product heading
([`.manual/` chapters](../.manual/INDEX.md); status in
[chapter 01](../.manual/chapter-01-goals-and-status.md)), and how to work
around it for now.

**Naming:** *materialize* = write the plan as files into `--target`
(former doc name *amalgamate*, renamed 2026-11-04). The CLI command for
that step stays `plr apply`. `plr build` is the one-shot full pipeline
(scan → group → plan → apply).

| # | Missing feature | Status | Bypass |
|---|-----------------|--------|--------|
| 1 | S6 partition in `increment` (present/matched/new actions, name-collision prefixes, ambiguity conflicts) | Implemented (core) | `plr increment` no longer re-aggregates the library: incoming images are grouped among themselves, then `group.Partition` classifies each member against the existing groups — present (sha or exact phash+size hit, joins the group it duplicates), joined (closest group that passes the `--identity` pair rule — bucket proximity alone never joins), new (own group). Library groups never merge through incoming images. Ties: smallest distance wins; equal distance to several groups joins the lowest id and prints an ambiguity warning. Action marking (`new`/`renamed`/`updated`/`untouched`, spec S7) is done at apply time by the apply ledger (row 14). Deferred: marking ambiguity as a plan conflict. Spec: [chapter 16](../.manual/chapter-16-merge.md). |
| 2 | Commit model: atomic snapshot, commit log, rollback | Implemented | Every plan state can be frozen: `plr commit [--message TEXT]` (run automatically by `build` and `increment` after the plan phase) snapshots the whole plan table in one transaction and prints the per-group diff (new/updated/removed) versus the previous commit. `plr rollback [--to N]` (default: the latest commit) restores an earlier snapshot and records the restore as a new commit, so the log stays append-only; image group assignments that point at groups the restore drops are zeroed. Scanned images are never touched. |
| 3 | `--move` for apply | Implemented | `--move` (apply, build, increment) deletes each source file once its copy with merged EXIF is fully in place. Duplicates inside a group and skipped conflict groups are left untouched; a failed source removal is reported (both files remain, data is safe). Dry-run prints "would move" and deletes nothing. Moving into the source directory renames the copy in place. Ledger-untouched outputs never delete their sources (they were placed by an earlier run, possibly without `--move`). |
| 4 | `report.json` written into the result directory | Implemented | Apply writes `report.json` into `--target`: the run summary (per-file source/dest/action/group, copied/updated/untouched/moved/renamed/skipped counts, errors) plus every group's plan (status, reason, capture, GPS, pixel source). Dry-run writes nothing. Apply also *reads* it once to adopt a target materialized before the apply ledger existed. |
| 5 | Client/server split (gRPC, job queue, SSE) | Not implemented | The CLI is a single binary; every phase reads and writes the same SQLite file. No bypass needed — this is the MVP form of the system. |
| 6 | ExifTool as the metadata engine | Implemented | `internal/exif` embeds the real ExifTool 13.59 (WebAssembly, pure Go, no cgo): reads every available tag, writes the merged facts, preserves all other tags. Built-in `meta`/`exifwrite` remain the fallback when the engine cannot start. Writes always go to a new file (in-place engine writes are unsafe — see [dependencies](dependencies.md)). |
| 7 | Banding candidate index for large libraries | Implemented | `group.Aggregate` no longer runs the all-pairs hamming pass: the 128-bit hash is split into `maxHamming+1` contiguous bands (`phash.Hash.Band`, width capped at 64 bits), and by the pigeonhole principle two hashes at distance ≤ `maxHamming` share a whole band. Only bucket mates are exact-tested, so grouping is near-linear; it stays exact for every radius, including exact (0) and negative (skipped). |
| 8 | phash cache keyed by (path, size, mtime) | Implemented | Forced re-scans compare each file's (size, mtime) with the stored row (new `mtime` column, unix nanoseconds): unchanged files keep their stored sha256/phash/EXIF facts and are never re-read, re-sha256'd or re-fingerprinted — `plr scan` counts them as `cached`. Changed files go through the normal fingerprint path. |
| 9 | HDBSCAN/OPTICS, `--min-cluster-size` | Intentionally omitted | Chain aggregation with `--max-hamming-chunking` is the clustering engine; tune the radius per library. |
| 10 | Copying extra camera tags (description, artist, copyright, ...) onto the output | Implemented | Engine writes preserve **every** metadata tag in the source (title, description, artist, copyright, camera fields, ...) while overwriting the merged facts; covered by `plr selftest` check 6. |
| 11 | `--conflict-hamming` flag | Removed | The near-identical rule conflicted exactly what the identity graph merges (hash distance 1–2 at equal capture time) and was dropped with the [identity decision graph](identity-decision-graph.md). |
| 12 | Alternate perceptual-hash types (Czkawka algs + sizes) | Implemented | `internal/phash` / `--phash-alg` / `--phash-size` now cover: **dct** (legacy 16×8 default, or square 8/16/32), and Czkawka/image_hasher **mean**, **median**, **gradient**, **vert-gradient**, **double-gradient**, **blockhash** at sizes **8 / 16 / 32**. DoubleGradient packs horiz then vert comparisons on an (N/2+1)² Lanczos3 grid (40/144/544 bits). Against DedupTestSet, DCT alone still plateaus (~F1 0.875); remaining misses need the candidate/identity architecture below, not a larger radius. |
| 13 | Identity strategies: refine candidate clusters before the EXIF plan | Implemented (`vision` is a stub) | `group.Pipeline` chains Aggregate (DCT candidates) → `--identity` refine → plan. `exif-first` (the default, see [identity decision graph](identity-decision-graph.md)) joins members that are sha256-equal or pass the dual-hash gate (blockhash-16 ∩ double-gradient-16, radii `--identity-block-hamming` / `--identity-grad-hamming`, defaults 10/10), compared **min-rotation**: one hash per algorithm is stored on the stored pixels and rotated in bit space (`phash.MinRotDistance`), the radii carrying fixed slacks for rotation + rescale noise (effective 96/28, see identity_exif.go); EXIF only splits a gate-passing pair (same capture instant + camera-block mismatch); `vision` keeps the candidate intact until a real Kronk `VisionDecider` ships. Scan writes all three hashes per decode into `content_phash` keyed by **sha256** (identical bytes from different paths share one row) — libraries scanned with the short-lived *canonical rotation* build need `plr scan --force` once. |
| 14 | Idempotent apply / no double materialization | Implemented | Apply keeps an **apply ledger** (`group_outputs` table, keyed by target dir): the file name it wrote per group plus fingerprints (pixel sha256 + merged-EXIF signature). Each group gets an action — `new` (place), `untouched` (fingerprints match, leave the file alone), `updated` (pixel source or plan facts changed: rewrite **under the recorded name**), `renamed` (plan's stored name changed: move the file in place, re-encode only if stale). A `%06d_` collision prefix is assigned **once** at the first real collision and never chains; a re-group that renumbers groups adopts its ledger row through the recorded pixel image id. Re-applying an unchanged library copies nothing; a target from a pre-ledger apply is adopted (names trusted, unknown fingerprints) from its `report.json` instead of being duplicated. |
| 15 | Capture agreement: unknown timezone offsets + `max_capture_disagreement` | Superseded by [identity decision graph](identity-decision-graph.md) | `--max-capture-disagreement` is gone (zone inheritance, once removed, is back — see below). The dual-hash gate joins every pair; `--same-image-window` (default 1 s) now only bounds the camera-block split: at Δt ≤ window a recorded camera-block (Make + Model) mismatch keeps a gate-passing pair apart, and Δt beyond the window never vetoes. A member with an unknown offset inherits the partner's known zone, so wall clocks compare on one zone. Resolution keeps the earliest zone-resolved EXIF instant; a member without EXIF (birth guess = `min(creation, modification)` time) created more than 5 s before that instant marks a data-quality conflict (the group still applies). |
| 16 | sha256 identifies processed content | Implemented | A scanned file whose sha256 is already in the library **and** whose path has no row of its own counts as already processed: the scan records no row and never reads the file past the hash (`plr scan` counts it as `already-known`). Byte-identical content arriving twice inside one scan (empty starting snapshot) is still recorded and grouped normally; a path that already owns a row refreshes as before. Forced re-scans drop vanished rows *before* snapshotting known content, so moving a file inside a scanned source keeps it recorded at its new path. |

## Notes

- The "file creation time precedes its own EXIF capture time" conflict (own
  or EXIF-less member predating the group's EXIF capture) is
  filesystem-dependent (birth time vs. mtime fallback), so the selftest suite
  exercises only the deterministic conflict kinds (GPS disagreement,
  undecodable pictures without EXIF).
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
2. **Candidates** (done) — `Aggregate` on DCT phash (`--max-hamming-chunking`) proposes
   clusters (banding index), plus the EXIF timestamp sweep for recall.
3. **Identity strategies** (done; `vision` stub) — `--identity`: `pass`
   (legacy), `double-phash` (intersect blockhash∩double-gradient by sha256),
   `vision` (Kronk stub: keep candidate until a real VisionDecider returns
   Same).
4. **EXIF / conflict plan** — existing `ResolveGroup` (offsets, GPS,
   near-identical); keep conflicts in the library. Apply never skips
   them: conflict groups materialize as a plain copy (reason in
   `report.json`).
5. **Vision fallback** — Kronk pairwise identity later; conflict `--ai`
   resolver already exists.

Diameter-bounded clusters remain a follow-on hardening.
