# Increment performance plan (from 2021-12-09 profile)

Source profile: `~/Repos/photo-library-data/profiles/increment-2021-12-09.{cpu,mem}.pprof`  
Run wall time ≈ **14.6 h** · CPU samples ≈ **52 661 s** · Outcome: success  
`increment: 147485 incoming → 17162 present, 124378 joined, 2031 new`

## Profile findings

| Hotspot | Flat / cum | Role |
| --- | ---: | --- |
| `group.Partition` → `classify` | — / **85%** | Walk every lib member per incoming |
| `ExifFirst.SameFunc` → `sameImagePair` | — / **78%** | Dual secondary gate on each pair |
| `phash.MinRotDistance` / `Rotations` | — / **77%** | Recompute 90° bit rotations every call |
| `phash.rotBlockhash90` | **30%** / 49% | Allocates a new `Hash` per rotation |
| `runtime.mallocgc` / `memclr` / `makeslice` | large | Rotation + slice alloc churn |
| `group.Aggregate` (incoming-only) | **≪1%** | Already parallelized — fine |
| Scan / fingerprint | ~**5.7%** | Secondary |
| Apply / EXIF write | small | After group |
| End-of-run heap | ~23 MB | Dominated by `report.json` marshal — not the group spike |

Root cause: with `same != nil`, `classify` runs the expensive identity gate **before** any primary-hash prune, against **every** library image (~147k × ~106k ≈ **15.7B** pair checks). Each check builds fresh rotated hashes (`rotBlockhash90` allocates).

```text
classify(incoming)
  for each lib member:          // O(|lib|)
    same(im, lib[i])            // MinRot × 2 algs × 3 rotations + alloc
    then maybe primary Hamming
```

## Implementation items

### P0 — Candidate index for Partition (algorithmic; largest win)

**Goal:** Only run `SameFunc` / min-rotation on a short candidate list.

1. Build a **sha256 → []LibMember** map; exact present hits in O(1).
2. Build the same **band index** Aggregate uses on primary DCT phash for the library (and optionally one medoid / representative per `group_id` first).
3. For each incoming image:
   - sha256 hit → `present` (no gate).
   - else gather band-mates within `maxHamming`.
   - for 90° tolerance: either index **all four rotations** of each lib primary/secondary hash into bands, or run `SameFunc` only on band candidates + a small “far but same” path if still required by the decision graph.
4. Reorder `classify`: cheap filters → gate, never gate → filter.

**Acceptance:** Partition on this corpus finishes in minutes (not hours); unit tests keep Partition semantics (present / matched / unmatched / ambiguous).

**Parallelize:** shard incoming components across workers (read-only lib index); merge results.

### P1 — Cache rotations / avoid alloc in `MinRotDistance`

**Goal:** Cut the 30%+ in `rotBlockhash90` and GC pressure.

1. Precompute `Rotations(h, alg)` once per content hash (or per image) and store beside `content_phash` or in an in-memory cache keyed by sha256.
2. Change `rotBlockhash90` / `rotDoubleGradient90` to write into a **pooled buffer** (or return views) instead of `make(Hash)` every call.
3. `SameFunc` cache already holds `dual`; extend it to hold **pre-rotated** duals.

**Acceptance:** CPU profile share of `rotBlockhash90` drops sharply on a Partition microbench; zero semantic change in `MinRotDistance` tests.

### P2 — Partition over group representatives

**Goal:** Shrink `|lib|` from ~all images to ~group count (12k vs 106k).

1. Index one (or few) representatives per `group_id` (e.g. medoid / planned pixel).
2. On match, assign that `group_id`; keep ambiguous rules.
3. Optional verify against other members only if ambiguous or distance near threshold.

**Acceptance:** Same merge outcomes on fixtures; large speedup when groups are multi-member.

### P3 — Parallelize Partition / Refine

1. `Partition`: parallel `classify` over incoming members with thread-local best matches (lib index immutable).
2. `ExifFirst.Refine`: parallelize pairwise loops inside large components, or cap component size after Aggregate.
3. Keep union-find / assignment serial or use locked batching.

### P4 — Scan index (done / follow-through)

Mid-walk `known` sha256 growth (already landed) prunes fingerprinting of byte-identical copies in one scan. Remaining scan cost is real new content (~5–6% here) — optional later: persist a phash band index in SQLite for the next increment’s Partition.

### P5 — Observability

1. Phase timers (`scan` / `Aggregate` / `Refine` / `Partition` / `plan` / `apply`) to stderr.
2. Counters: candidates/incoming, SameFunc calls, sha256 short-circuits.
3. Checkpoint `SetGroupAssignments` in batches so a kill does not lose all group work (optional).

## Suggested order

1. **P0** band + sha256 index in Partition (must-have).  
2. **P1** rotation cache / pool (multiplies with P0; cheap alone).  
3. **P2** group representatives.  
4. **P3** parallel Partition.  
5. **P5** timers/counters while doing P0–P1.

## Out of scope

- Vector DB (wrong distance model for Hamming phash).  
- Replacing ExifFirst decision graph.  
- Further Aggregate rewrites (already fine on this profile).
