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
| 3 | `--move` for apply | Not implemented | Copy is the safe default. If a move is wanted, delete the source file manually after applying; the plan records which source file provided each stored image (`plr report`). |
| 4 | `report.json` written into the result directory | Not implemented | `plr report` prints the equivalent summary (sources, images, per-group plans, conflicts). |
| 5 | Client/server split (gRPC, job queue, SSE) | Not implemented | The CLI is a single binary; every phase reads and writes the same SQLite file. No bypass needed — this is the MVP form of the system. |
| 6 | ExifTool as the metadata engine | Pure-Go reader (`internal/meta`) and writer (`internal/exifwrite`) instead | Covers the tags the pipeline needs (capture time, offset, GPS, orientation). Full ExifTool tag parity (e.g. copying camera description/copyright tags) is out of scope; those tags are dropped from the output. |
| 7 | Banding candidate index for large libraries | O(n²) pairwise hamming in `group.Aggregate` | Fine for MVP sizes (low tens of thousands of images). If grouping gets slow, add a band index before the exact pairwise pass. |
| 8 | phash cache keyed by (path, size, mtime) | Not implemented | Re-scanning re-hashes every file. The directory skip (`--force` required to re-scan) limits the cost; a forced re-scan after edits is the supported workflow. |
| 9 | HDBSCAN/OPTICS, `--min-cluster-size` | Intentionally omitted | Chain aggregation with `--max-hamming` is the clustering engine; tune the radius per library. |
| 10 | Copying extra camera tags (description, artist, copyright, ...) onto the output | Not implemented | Output EXIF carries only the resolved capture time, offset, orientation and GPS. |
| 11 | `--conflict-hamming` flag | Parsed but unused | The plan phase has no rule that consumes it; the flag is kept for interface parity. Conflicts come from EXIF disagreement and the creation-time-vs-capture check. |

## Notes

- The "file creation time precedes its own EXIF capture time" conflict is
  filesystem-dependent (birth time vs. mtime fallback), so the selftest suite
  exercises only the deterministic conflict kinds (offset disagreement, GPS
  disagreement).
- Selftest groups with a phash radius of 16 bits (the CLI default is 12) so
  quality re-encodings of the same picture always chain; the checks verify
  the resolution rules, not the default radius.
