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
| 2 | Commit model: atomic snapshot, commit log, rollback | Not implemented | Copy the database before a risky phase: `cp plr.db plr.db.bak`. Plans live in the `groups` table; dropping the backup and re-running `plr plan` recovers. |
| 3 | `--move` for apply | Implemented | `--move` (apply, build, increment) deletes each source file once its copy with merged EXIF is fully in place. Duplicates inside a group and skipped conflict groups are left untouched; a failed source removal is reported (both files remain, data is safe). Dry-run prints "would move" and deletes nothing. Moving into the source directory renames the copy in place. |
| 4 | `report.json` written into the result directory | Implemented | Apply writes `report.json` into `--target`: the run summary (per-file source/dest, copied/moved/renamed/skipped counts, errors) plus every group's plan (status, reason, capture, GPS, pixel source). Dry-run writes nothing. |
| 5 | Client/server split (gRPC, job queue, SSE) | Not implemented | The CLI is a single binary; every phase reads and writes the same SQLite file. No bypass needed — this is the MVP form of the system. |
| 6 | ExifTool as the metadata engine | Implemented | `internal/exif` embeds the real ExifTool 13.59 (WebAssembly, pure Go, no cgo): reads every available tag, writes the merged facts, preserves all other tags. Built-in `meta`/`exifwrite` remain the fallback when the engine cannot start. Writes always go to a new file (in-place engine writes are unsafe — see [dependencies](dependencies.md)). |
| 7 | Banding candidate index for large libraries | O(n²) pairwise hamming in `group.Aggregate` | Fine for MVP sizes (low tens of thousands of images). If grouping gets slow, add a band index before the exact pairwise pass. |
| 8 | phash cache keyed by (path, size, mtime) | Implemented | Forced re-scans compare each file's (size, mtime) with the stored row (new `mtime` column, unix nanoseconds): unchanged files keep their stored sha256/phash/EXIF facts and are never re-read, re-sha256'd or re-fingerprinted — `plr scan` counts them as `cached`. Changed files go through the normal fingerprint path. |
| 9 | HDBSCAN/OPTICS, `--min-cluster-size` | Intentionally omitted | Chain aggregation with `--max-hamming` is the clustering engine; tune the radius per library. |
| 10 | Copying extra camera tags (description, artist, copyright, ...) onto the output | Implemented | Engine writes preserve **every** metadata tag in the source (title, description, artist, copyright, camera fields, ...) while overwriting the merged facts; covered by `plr selftest` check 6. |
| 11 | `--conflict-hamming` flag | Implemented | Plan rule: any pair of group members whose perceptual hashes are within `--conflict-hamming` bits (default 2) and whose capture instants are identical marks the group as a conflict ("near-identical pictures … with identical capture time"); a negative limit disables the rule. Composes with the EXIF-disagreement conflicts, which take precedence; the AI resolver can settle the result. |

## Notes

- The "file creation time precedes its own EXIF capture time" conflict is
  filesystem-dependent (birth time vs. mtime fallback), so the selftest suite
  exercises only the deterministic conflict kinds (offset disagreement, GPS
  disagreement).
- Selftest groups with a phash radius of 16 bits (the CLI default is 12) so
  quality re-encodings of the same picture always chain; the checks verify
  the resolution rules, not the default radius.
