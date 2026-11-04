# Defaults & tunables

Single canonical record of every tunable and its default. Code is the
source of truth (`internal/cli/cli.go` flags, named constants in
`internal/group/`); this page mirrors it — update here when a default
changes.

## CLI flags

| Flag (command) | Default | Meaning |
| --- | --- | --- |
| `--db` (all) | `plr.db` | SQLite library path |
| `--force` (scan, build, increment) | off | re-scan already-scanned dirs (also re-hashes; needed after a hash-definition change) |
| `--workers` (scan) | `4` | parallel hash workers |
| `--min-size-kb` (scan) | `0` (off) | ignore smaller images |
| `--phash-alg` (scan) | `dct` | primary (candidate) hash algorithm; the secondary gate hashes are stored regardless (see constants) |
| `--phash-size` (scan) | `0` = legacy 16×8 DCT (128 bits) | square hash size 8/16/32 |
| `--max-hamming-chunking` (group, build, increment) | `20` | candidate bucket radius on the primary hash — **chunking only, never identity** |
| `--identity` (group, build, increment) | `exif-first` | identity strategy (`pass`, `double-phash`, `vision` also exist) |
| `--same-image-window` (group, build, increment) | `1s` | zone-resolved capture gap meaning "same instant": drives the camera-block split veto **and** the timestamp candidate sweep |
| `--identity-block-hamming` | `0` → strategy default (`10` exif-first, `24` double-phash) | configured blockhash-16 radius; **add the built-in slack below** |
| `--identity-grad-hamming` | `0` → strategy default (`10` exif-first, `20` double-phash) | configured double-gradient-16 radius; **add the built-in slack below** |
| `--ai` (plan) | off | settle conflict groups with the Kronk decision model |
| `--model` (plan) | `chaoliangUNSW/Jev-Style-0.8B-Decision-v3-Q8_0` | Kronk decision model source |
| `--target` (apply, build, increment) | — (required) | materialize destination |
| `--dry-run` / `--no-exif` / `--move` (apply) | off | report-only / skip EXIF write / delete source after copy |
| `--message` (commit) | `plan` | commit summary |
| `--to` (rollback) | latest commit | commit id to restore |

There is **no** `--force-conflicts`: conflict groups always materialize
as a plain copy of the best-effort pixels, with the reason recorded in
`report.json`.

## Built-in constants (not flags)

| Constant | Value | Where | Meaning |
| --- | --- | --- | --- |
| `slackBlockhash` | `86` | `identity_exif.go` | added to the blockhash-16 radius for min-rotation + rescale noise |
| `slackGradient` | `18` | `identity_exif.go` | added to the double-gradient-16 radius likewise |
| effective gate radii | exif-first **96 / 28**, double-phash **110 / 38** (block / gradient) | — | configured default + slack, applied under `phash.MinRotDistance` (best of the four bit rotations; the primary DCT is never rotated) |
| `captureTolerance` | `5` s | `group.go` | a file's birth may precede its own EXIF capture by at most this; an EXIF-less member born more than this before the group's earliest EXIF capture is a birth-time conflict |
| gate hashes | blockhash-16 + double-gradient-16, always | scan | written to `content_phash` per sha256 next to the primary hash, independent of `--phash-alg` |

## Calibration (why the numbers are what they are)

Measured on the `photolibfix_sample` corpus (151 files, 66 ground-truth
identities) plus scratch harnesses; score = over-merged / under-merged
identities. Current default: **2 / 1** (overs are pixel-identical
double-label artifacts; the under is the documented recall gap,
[identity decision graph §8](identity-decision-graph.md)).

| Pair class (same picture unless noted) | blockhash-16 | double-gradient-16 |
| --- | --- | --- |
| downscale / thumbnail re-save | 0–6 | 16–27 |
| 90°/270° re-save, re-encoded + resized | 56–90 | 3–6 |
| hash-bit rotation tax alone (no re-encode) | 0–72 | 0–6 |
| **different** pictures | 66–128 | ≥62 |
| exports **cropped** from one photo (different pictures) | 48 | 29–30 |

The gradient radius (28) sits between the same-picture worst case (27)
and the crop pair (29); blockhash must reach 90 or the rotated re-saves
get vetoed — the gradient carries the separation, blockhash only vetoes
gross disagreement. Thin margins are inherent (crop ≈ same picture),
see the decision graph §7 before changing any of these.
