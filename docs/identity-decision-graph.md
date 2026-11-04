# Decision Graph — duplicate identity & group resolution

Status: **implemented** (2026-11-03, `internal/group/identity_exif.go` +
`group.go`). Approved spec drift from `.manual/chapter-02-semantics.md`
§2.3/§4.5: EXIF capture time is the first-order identity of a duplicate;
perceptual hashes are the fallback. Supersedes the ≥2-EXIF spread rule
(`--max-capture-disagreement`) and the near-identical conflict rule
(`--conflict-hamming`). The 2026-11-03 review removed the EXIF veto
(§1.3) and added the camera-block check; a corpus pass then re-enabled zone
inheritance and switched the EXIF-less birth guess to `min(creation,
modification)`. A further corpus pass inverted the order: **the dual-hash
gate decides every pair; EXIF only splits** — the pair rule is now:
sha256, *or* (dual-hash gate *and not* same-instant camera conflict).
The same pass made matching rotation-tolerant (see §7).

## 1. Principles

1. **Bucketing is chunking, not identity.** 8×8-DCT phash (current 128-bit
   hash) clusters candidates into small chunks; membership in a chunk says
   "worth comparing", nothing more.
2. **The dual-hash gate is the join.** A pair is the same image only if
   *both* 16×16 perceptual distances pass — a **block** hash (coarse
   luminance blocks) and a **gradient** hash (edge/direction structure),
   each under its own radius (block tolerant of color and scaling;
   gradient tolerant of recompression; the conjunction is strict). A
   shared capture second is not enough on its own: burst frames are
   distinct pictures one second apart, and they pass the hash gate only
   when they really look alike.
3. **EXIF can split, never force.** Two members whose *zone-resolved*
   capture instants differ by ≤ **1 second** *and* whose recorded *camera
   blocks* (EXIF Make + Model) both exist and differ are two cameras
   firing at one instant — the pair stays split even when the hashes
   agree. Any other EXIF fact (including Δt > 1 s) is no veto: the
   dual-hash gate decides.
4. **Undecidable stays undecidable.** When a member lacks a decodable
   16×16 hash, the gate has no evidence and the pair is not joined on
   EXIF alone; it goes to the resolver / conflict path instead.
5. **Matching is transitive.** Same-image edges get a transitive closure —
   the goal is to *collect all duplicates*, so A~B and B~C put A, B, C in
   one group even when A~C alone would fail (chain drift is accepted;
   see §6).

## 2. Flow atop the library

```mermaid
flowchart TD
    L[library images<br/>sha256 · 8x8 phash · EXIF facts] --> B

    subgraph BUCKET[Stage 1 · bucket — chunking only]
        B[8x8-DCT phash band index<br/>union at distance ≤ bucket-radius] --> BK[buckets of candidates]
    end

    BK --> P

    subgraph PAIRWISE[Stage 2 · pairwise identity — decision graph §3]
        P[for every pair inside a bucket:<br/>dual-16x16 gate, then EXIF split] --> E[same-image edges]
    end

    E --> C[Stage 3 · transitive closure<br/>union-find over same-image edges]
    C --> G[duplicate groups<br/>buckets split; distinct shots separated]

    subgraph RESOLVE[Stage 4 · resolution per group]
        G --> R1[pixels: HEIC first, else most pixels,<br/>tie file size]
        G --> R2[capture: earliest zone-resolved EXIF instant<br/>the group's single known zone is inherited<br/>all EXIF-less → birth-time fallback, flagged]
        G --> R3[GPS: supplementary — positions agreeing<br/>within tolerance → medioid; else conflict<br/>missing = no information, inherits]
        G --> R4[name: camera-name heuristic]
    end

    R1 & R2 & R3 & R4 --> PLAN[plan / ledger / apply]
```

Note the structural change from today: **buckets are not groups**. A
bucket may split into several groups (burst frames separate), and Stage 3
closure operates on same-image edges, not on bucket membership.

## 3. Pair decision graph

Zone resolution: every member is parsed against **its own** recorded
offset. When exactly one side records an offset, the other inherits it — a
missing zone is no information, and a pair was shot in one place — so their
wall clocks compare on the same zone. Two *known* offsets compare as instants
directly (equal instants across zones are the same moment); two unknown ones
compare wall clocks. The "offsets differ" conflict disappears.

```mermaid
flowchart TD
    START[pair A,B in same bucket] --> Q0{same sha256 ?}
    Q0 -- yes --> SAME0[SAME IMAGE]
    Q0 -- no --> Q3{both images decodable<br/>16x16 block + gradient?}
    Q3 -- no --> UNDECIDABLE[undecidable<br/>→ conflict / resolver]
    Q3 -- yes --> Q4{block16 distance ≤ T_block<br/>AND<br/>gradient16 distance ≤ T_grad ?}
    Q4 -- no --> DIFF2[NOT SAME]
    Q4 -- yes --> Q1{both have parsable<br/>EXIF capture?}
    Q1 -- "no (absent / unparsable)" --> SAME1[SAME IMAGE]
    Q1 -- yes --> Q2{zone-resolved<br/>Δt ≤ 1s ?}
    Q2 -- no --> SAME1
    Q2 -- yes --> Q5{both record a camera block<br/>and the blocks differ?}
    Q5 -- yes --> DIFF1[NOT SAME<br/>two cameras at one instant]
    Q5 -- no --> SAME1
```

Mixing cases: an EXIF member paired with an EXIF-less member takes the
dual-hash branch (the EXIF-less side has no instant to compare). When the
hashes pass, the pair merges and the **EXIF side donates capture time**
(and GPS) to the group.

## 4. Formal rule (pairwise, within one bucket)

Let `cam(X)` = camera block (Make + Model), `dB` = block16 Hamming, `dG` =
gradient16 Hamming. `inst(X)` is X's EXIF wall clock under offset `o(X)`; in a
pair, an empty `o` inherits the partner's when the partner records one (zone
inheritance), and two empty offsets both read as UTC wall clock.
`Δt = |inst(A) − inst(B)|`.

```
same(A,B) ≜
  sha256(A) = sha256(B) ≠ ∅          : true
  ¬ (dB ≤ T_block ∧ dG ≤ T_grad)     : false   (gate first, no hashes = no join)
  both EXIF, Δt ≤ 1s, camConflict    : false   (EXIF splits)
  otherwise                          : true

camConflict(A,B) ≜ cam(A) ≠ ∅ ∧ cam(B) ≠ ∅ ∧ cam(A) ≠ cam(B)
```

Groups = connected components of `same` restricted to bucket edges.
Resolution then runs per component (pixels/capture/GPS/name as in §2).

## 5. What this replaces in current code

| Current | Fate |
|---|---|
| `Aggregate` union on phash ≤ maxHamming *is* the group | stays, demoted to bucketing (Stage 1) |
| `resolveMultipleEXIF` spread rule, `--max-capture-disagreement` 10s | replaced by pairwise Δt ≤ 1s |
| "EXIF timezone offsets differ" conflict | gone — instants compared directly |
| "EXIF GPS positions differ" conflict | gone as veto — missing GPS inherits; differing GPS is supplementary: within tolerance → medioid, beyond → conflict (group still applies, §6) |
| `nearIdenticalPair` / `--conflict-hamming` conflict rule | **removed** — it conflicted exactly what §3 merges (d 1–2, Δt 0) |
| `captureTolerance` 5s birth-vs-EXIF conflict | kept as a data-quality conflict (never a split — the group still applies) and extended: a member *without* EXIF whose file predates the group's earliest EXIF capture time by more than 5 s also conflicts |
| unparsable EXIF → conflict | falls to dual-hash branch; conflict only if also undecodable |
| 16×16 block/gradient hashes | **new** — computed lazily, only for EXIF-less files; needs storage (columns or hash table) + decoder coverage |

## 6. Accepted consequences

- Burst frames ≤ 1 s apart merge only while they still pass the hash gate
  (they usually do — adjacent bursts look alike); a visible change inside
  the second now keeps them apart, which it previously did not.
- Two cameras firing at the same instant merge **only while at least one
  side records no camera block**; distinct recorded Make/Model split the
  pair even when the hashes agree (§1.3).
- Transitive chains can drift: A(12:00:00.0)~B(12:00:00.8)~C(12:00:01.6)
  merges A with C although Δt(A,C) > 1s. Accepted — collecting *all*
  duplicates wins.

## 7. Decisions (2026-10-29, user; extended 2026-11-03)

- **Window & flags** (Q4): pairwise same-image window default **1 s**;
  `--max-capture-disagreement` → `--same-image-window`, `--conflict-hamming`
  removed. Accepted.
- **GPS is supplementary** (Q3): never an identity veto. Within a resolved
  group, positions that agree within tolerance collapse to their
  **medioid**; positions that disagree beyond tolerance → **conflict**.
- **Conflict policy** (Q5): undecodable + no-EXIF pairs, and any conflict,
  **never drop the group** — apply materializes all members ("keep both")
  and records the conflict in the ledger/report. This also resolves the
  spec §2.3 vs code divergence found in the design review.
- **Thresholds** (Q1): no hand-picked values. czkawka's single-hash gate
  (64-bit, threshold ≈10) is not transferable to a 256-bit dual-gate
  conjunction and is precisely what produced its silent false positives.
  Calibration plan: label corpus pairs *by the EXIF rule itself*
  (zone-resolved Δt ≤ 1s ⇒ same image; burst-adjacent ⇒ different),
  compute block16/gradient16 distance distributions over positives and
  negatives, set T_block / T_grad at the separation. Run and report before
  wiring defaults.

- **2026-11-03 review** (user, implemented):
  - **No EXIF veto** — Δt > 1 s falls to the hash gate instead of
    forbidding the merge; only two rule changes beyond the hash gate:
    a recorded camera-block mismatch at Δt ≤ 1 s splits, and sha256
    equality always merges.
  - **Camera block** = EXIF Make + Model, stored per image
    (`images.exif_camera`); an empty block never vetoes.
  - **Zone inheritance (re-enabled)** — a member with an unknown offset
    inherits its partner's known zone (and the group's single known zone in
    resolution). The review removed this, but the corpus showed four
    identities split only for a missing `OffsetTimeOriginal`, so it is back;
    the re-run added no false merge.
  - **Birth-time conflicts extended** — an EXIF-less member created more
    than 5 s before the group's earliest EXIF capture time conflicts. The
    file birth guess is now `min(creation, modification)` time, so a copy
    that keeps its original's older mtime is not credited with its later
    copy moment.
  - **Pixel winner prefers HEIC** (original container), then most pixels,
    then larger file. Capture winner unchanged: earliest zone-resolved
    EXIF instant, else earliest creation time (flagged).
  - Increment uses the same pair rule: bucket proximity alone never
    joins a library group. The chunking flag is `--max-hamming-chunking`.
  - **Rotation hashing (superseded, 2026-11-03).** Lex-min canonical
    rotation (`phash.Canonical`) was implemented and then removed the same
    week in favour of min-rotation matching (below): a canonical form can
    flip a crop against its original and hides which rotation matched.
  - **Rotation-tolerant matching (implemented, 2026-11-04).** The corpus
    has **no** EXIF Orientation tag, but pixel-rotated re-saves are real.
    One hash per algorithm is stored — on the **stored pixels**, no
    canonical form — and compared under `phash.MinRotDistance`, which
    transforms the hash *bits* through each 90° turn instead of the
    pixels: blockhash rotates its 16×16 bit grid (180° bit-exact; 90°
    shifts the 4-row band medians, ≤90 bits of noise together with
    thumbnail resampling), double-gradient permutes H↔V and negates one
    half (a turn reverses one comparison order; ≤6 bits on rotated
    re-saves). The legacy 16×8 DCT has no rotation (coefficients negate,
    not permute) — it stays a bucket key only. Gate radii grow by
    `slackBlockhash`/`slackGradient` (identity_exif.go): effective 96/28
    at the 10/10 defaults; the gradient carries the separation (crops of
    one photo measure 29–30 gradient bits, unrelated pictures ≥62).
    Corpus score (over/under): 2/7 → **2/1** (overs are pixel-identical
    double-label artifacts; the remaining under is a seed-stage thumbnail
    with no EXIF whose candidate recall never meets its group — see §8).
    **Hash values change (canonical → stored pixels) — rescan with
    `plr scan --force` after upgrading from the canonical build.**
    Mirrors are not handled. Reported Width/Height stay the stored ones.
  - **Gate first, EXIF splits (implemented, 2026-11-03).** The corpus had
    over-merges created by the same-instant shortcut (two identities whose
    hashes are far apart merged on Δt ≤ 1 s alone). The order is now the
    dual-hash gate first, EXIF only splitting a pair the gate accepted
    (§1.2/§1.3). Corpus: over-merged 4 → 3 (remaining three are pairs of
    files duplicated into two ground-truth folders), under-merged 12 → 10
    (remaining: Instagram `_c` crops and the double-label artifacts).

## 8. Candidate generation (implemented)

Bucket-only recall ceiling: Stage 2 only sees pairs inside a bucket
(8×8 distance ≤ radius), so a same-EXIF pair whose 8×8 hash drifted past
the radius — extreme downscale + color grade, exactly the case §1.2 says
must still merge — is never compared and silently stays unmerged.

**Implemented fix — timestamp candidate path:** EXIF-bearing members also
enter a candidate index keyed by zone-resolved instant (sort + sweep,
candidates within the window, O(n log n)), so an EXIF-rule pair whose 8×8
hash drifted past the bucket radius is still compared. Hash buckets remain
the candidate source only for the EXIF-less fallback, which is their
proper role. Unknown-offset members are indexed at their wall clock in the
sweep (zone inheritance happens at comparison, and a drifted pair that misses
the sweep is still recalled by its hash bucket); hash buckets stay the second
opinion for those pairs.

**Known recall gap (open).** A member with **no** EXIF capture time whose
primary hash has drifted far past the bucket radius — a heavily re-encoded
rotated thumbnail (corpus case: 270×360 vs 3072×2304, DCT 64 bits apart,
while the dual-hash gate says blk 90 / grd 6, i.e. same picture) — reaches
neither candidate path: too far for the hash bucket, absent from the
timestamp sweep. It needs a secondary-hash candidate path (recall by
min-rotation blockhash/gradient neighbours, which the sweep would key
instead of by instant) before the gate's own radii get any use.
