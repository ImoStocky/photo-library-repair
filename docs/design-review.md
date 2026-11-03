# Design & Quality Review — plr Go implementation

Date: 2026-10-29 · Branch `go_rewrite` @ `499283c` · Scope: all `internal/`
packages + `main.go` (~9.5k lines incl. tests). Companion to
[`docs/mvp-gaps.md`](mvp-gaps.md) (tracked gaps) and
[`.manual/chapter-02-semantics.md`](../.manual/chapter-02-semantics.md)
(spec). **Review only — no code changed.**

Verified baseline: `go vet`, `staticcheck`, `go test ./internal/...` all
clean. Largest functions: `selftest.Check` 232 L (9 checks in one func),
`apply.Apply` 212 L, `group.ResolveGroup` 125 L, `cli.runPipeline` ~100 L.
`cli/cli.go` is 962 L; `selftest.go` 898 L; `store.go` 580 L;
`group.go` 530 L.

---

## 1. Complexity hotspots (what to decompose first)

Ranked by churn-risk × length:

1. **`cli/cli.go` — the real problem is duplication, not length.**
   `runScan/runGroup/runPlan/runApply` each parse a hand-maintained subset
   of flags and delegate to `runPipeline`, which re-parses **all** flags
   with `flag.ContinueOnError` (cli.go:298-397). Two sources of truth for
   every flag; easy to wire one path and forget the other.
   *Fix shape:* one `pipelineOptions` struct + one `registerPipelineFlags(fs)`
   called by both the per-command sets and the root switch; runners build
   the struct instead of re-delegating strings.
2. **`apply.Apply` (212 L, apply.go:102-314)** — five responsibilities in
   one loop: target layout, ledger lookup, staleness decision, write/rename
   I/O, and ledger/report bookkeeping. The `existing != nil` branch alone is
   ~55 L with three nested outcomes.
   *Fix shape:* extract `decideAction(live, existing, targetName) action`
   (pure — unit-testable without disk) and `materialize(...) outcome`; keep
   Apply as the loop + ledger writes.
3. **`selftest.Check` (232 L, selftest.go:34-267)** — nine independent
   checks in one function with shared `fail()` closure. Each check is
   already a logical unit.
   *Fix shape:* `[]struct{name string; run func(*strings.Builder) error}`;
   Check becomes a loop. Big readability win, zero behavior risk.
4. **`group.go` (530 L)** mixes four concerns: aggregate clustering,
   conflict resolution, naming/scoring, and EXIF-fact merging
   (`resolveMultipleEXIF` alone is 91 L). Spec itself says these are
   separate steps ("plan" vs naming).
   *Fix shape:* split into `aggregate.go` / `resolve.go` / `facts.go`;
   extract the per-field merge loops in `resolveMultipleEXIF` into a small
   `mergeField[T]` helper (the four loops are copy-adapt-copy).
5. **`store.go` (580 L)** — schema + opens + sources + images + groups +
   phash lookup in one file. Low complexity per function, but it will keep
   growing as the commit log matures.
   *Fix shape:* preemptive split `store.go` / `images.go` / `groups.go`
   (same package, no API change).
6. **`kronk.Ask` + `parseKronkJSON` (client.go)** — the JSON parser is a
   ~90 L hand-rolled brace scanner with three parallel stacks. `encoding/json`
   into a `json.RawMessage` struct (as `parseVisionJSON` already does) would
   replace most of it; the lenient "first balanced object" extraction is the
   only part worth keeping.

Everything else is comfortably small; most files read as one concept per
file (`partition.go`, `commit.go`, `hashcache.go`, `tzresolve.go`).

## 2. Design patterns — actual usage

Present and **earning their keep** (not cargo-cult):

- **Strategy** — `fingerprint.IdentityStrategy` (interface, `New` accepts a
  nil defaulting to `DefaultIdentity()`); `partition.HashStrategy` interface
  with `DCTPhashStrategy` + `ExactSHA256`. Well-scoped, test doubles are
  trivial. Matches mvp-gaps "Architecture direction".
- **Resolver** — `exif.Resolver` as `ResolverFunc` is the idiomatic
  func-adapter variant; `group.Resolution`/`Conflict` as a result type
  mirrors it. This is the one pattern the spec names (§3.3), and it is
  honored.
- **Adapter** — `storePhashLookup` (cli.go:653) adapts the store to
  `partition.PhashLookup`; `partitionAdapter` adapts `Resolver` to
  `partition.ConflictResolver`. Both are 10-line glue types doing exactly
  what adapters should.
- **Options struct / functional defaults** — `ingest.Options`,
  `partition.Options`, `apply.Options`, `kronk.Options` all follow
  zero-value-means-default with explicit `withDefaults`. Consistent.
- **Ledger as write-ahead record** — `group_outputs` before `os.Rename` is
  effectively WAL discipline applied to the filesystem side (see §4).

**Missing or strained:**

- `apply.Options.EXIF` is the concrete `*exif.Tool`, while
  `fingerprint.Options.EXIF` is the `Resolver` interface (DIP inconsistency;
  apply is the one place you'd want to stub EXIF in tests — it currently
  stubs by writing real JPEGs).
- No injection seam for the filesystem in `apply` (`os.WriteFile`,
  `os.Rename` called directly) — forces integration-style tests. Fine for
  now given selftest quality; worth a `fileSystem` interface only when
  apply grows another branch.
- `identity.Fingerprint`→`FileFact` conversion is duplicated conceptually
  in `fingerprint` (facts) and `partition` (filename-only `FileFact`) —
  two `FileFact` types with overlapping fields (`identity.FileFact` vs
  `partition.FileFact`). A shared facts type in a small leaf package would
  remove the silent-mapping risk (e.g. `partition.FileFact.Matches` uses
  `Bytes` for "size", which is not in `identity.FileFact`).

No over-engineering found: no premature interfaces, no factory factories.
The codebase leans KISS and that is mostly right.

## 3. SOLID (pragmatic reading)

- **SRP** — violated in `cli.go` (flag parsing × N + orchestration),
  `apply.Apply` (deciding + doing + recording), `selftest.Check` (×9),
  and `runPipeline` (pipeline + report rendering). §1 gives the splits.
- **OCP** — good at the strategy seams (new hash = new `HashStrategy`);
  poor at conflicts: adding a conflict kind means touching `ResolveGroup`,
  `apply.Apply` (its `strings.Contains` switch — see §5), and tests.
- **LSP** — no issues; implementations honor their interfaces.
- **ISP** — `partition.PhashLookup` (one method) and `exif.Resolver`
  (one method) are textbook. `store.Store` is a concrete god-type rather
  than an interface — acceptable (single implementation, package-private
  consumers), but it means the ~10 store methods `apply`/`partition` use
  aren't contract-checked.
- **DIP** — inward deps flow correctly (`cli → group/partition/apply →
  fingerprint/phash/identity → exif`; `exif` depends on nothing internal;
  `kronk` is a leaf). Two leaks: `apply → *exif.Tool` concrete (above),
  and `cli` re-implementing domain decisions (naming via `planName`
  duplication, conflict classification via string matching).

## 4. KISS, ACID, and error handling

**KISS** — generally good: plain structs, explicit loops, no reflection
magic. Exceptions: the brace-scanner JSON parser (§1.6); the parallel-arrays
report model in `runPipeline` (five `[]string`/`[]apply.Result` slices that
must stay index-aligned — a small struct would make the invariant
structural); `pickName`'s four-criteria tuple compare is clever but would
read better as a named `nameScore` comparison.

**ACID** — the SQLite side is genuinely careful:

- `journal_mode=WAL`, `foreign_keys=ON`, `busy_timeout=10s`, MaxOpenConns=1
  (store.go:58-64) — correct for this workload.
- `Tx` has `ErrCommitted`/`ErrRollbackOnly` guards and commit/rollback
  panic wrappers (commit.go) — a mature little transaction helper.
- **Gap:** the plan phase is *not* one transaction. `runGroup` →
  `PartitionLibrary` does `ClearGroups()` → N× `SaveGroup` + N×
  `SetGroupAssignments` + N× `SetConflict` as independent statements
  (partition.go:223-250). A crash mid-plan leaves groups cleared and only
  partially rewritten — next scan sees a half-populated library. The
  commit-log machinery (`Tx`) already exists and is used for
  `CommitPartition`; wiring the live-library mutation path through it is
  the single highest-value ACID fix.
- **Gap:** `UpsertImage`/`IndexFile` (store.go:199-296) do SELECT → INSERT
  or UPDATE without a transaction; concurrent scanners could interleave.
  Single-process today, so low severity, but the UNIQUE constraints exist
  precisely for this — an `INSERT … ON CONFLICT DO UPDATE` would collapse
  80 lines to 15 and make it atomic.
- **Crash-safety direction is right:** ledger rows are written *after* the
  file lands, so a crash can only cause a benign rewrite, never a phantom
  "written" row pointing at a missing file. Document this as intent.
- `commit_log` is written but never replayed (mvp-gaps row 8) — fine, the
  code comments say so.

**Error handling** — consistent `%w` wrapping; errors-as-values throughout;
context checks in the Kronk path. One real bug: `os.Rename` error path
(apply.go:254-262) rewrites the ledger row with `oldPrefixed` — correct —
but only *after* the rename failed, which is right; however if that ledger
rewrite *also* fails, the returned error loses the rename error (it is
dropped with `_ =`). Wrap-both instead.

## 5. Spec ↔ code mismatches

Ordered by severity. "Gap" items already tracked in mvp-gaps are marked;
new findings are marked **NEW**.

1. **NEW — §2.2 exact-duplicate dedup is not implemented at all.** The
   spec requires the same bytes (magic-sniffed, not extension) to be one
   exact-duplicate group with the original never re-emitted. There is no
   exact-hash strategy in `partition` (only `ExactSHA256` exists but is
   never wired — `New` is dead code), and `Aggregate` treats same-bytes
   files as "multiple pixels, no EXIF" rather than exact duplicates.
   `mvp-gaps.md` has *no row* for this — the closest, row 5, is about
   cross-source duplicates, a different feature.
2. **NEW — hardlink semantics inverted.** Spec §2.2/§2.5: hardlinked
   files are "decoded once, still listed in each directory". Code
   (`ingest.scanWorker`, ingest.go:182-196) *skips the second path
   entirely* — it never enters the DB, so it can never be "listed".
3. **NEW — detection is by extension, not magic bytes.** Spec §2.1:
   "Detection is by content (magic bytes), not by suffix". `hasImageExt`
   gates before any decode; a JPEG named `.txt` is invisible (mvp-gaps
   row 13 tracks "suffix-only detection" as pending — consistent).
4. **NEW — §2.5 "same size" compares pixel dimensions, not bytes.**
   `sameSize` (partition.go:148-150) compares `Width/Height`. Spec §4.5
   defines "same size" as "same decoded pixel dimensions", so the code is
   arguably *more* correct than §2.5's shorthand — but §2.5 as written
   diverges; pick one wording in the spec.
5. **NEW — pixel selection ignores encoding rank.** Spec §4.5: among
   same-size candidates prefer "largest → best encoding HEIC>JPEG>TIFF>
   PNG>WebP>GIF → highest resolution". `choosePixel` breaks ties by
   smallest path only; `formatRank` exists solely for naming. Also §4.5's
   "highest resolution" is ambiguous vs "same size" and unimplemented.
6. **NEW — name selection ≠ spec rule.** Spec §4.5: "shortest basename
   after case folding and extension removal, path as tie-break". Code
   uses the `nameScore` heuristic (date-like/keyword/length penalties) +
   shortest-path. Reasonable, but it is a *different rule*; the spec's
   case-folded comparison is also absent from `nameScore` (only the
   path-length tie-break case-folds).
7. **NEW — GPS rule differs.** Spec §4.5: "smallest-latitude, then
   smallest-longitude among present coordinates". Code picks GPS from the
   capture-time file and merges others within 1e-5°.
8. **NEW — camera-file detection unimplemented.** Spec §2.3 defines
   camera files by the camera-tag set and "most camera fields" for
   capture/clock selection. `identity.IsCameraFile` only checks
   `len(ExifTags) > 0`; no camera-tag set exists anywhere.
9. **NEW — conflicts drop the group at apply.** Spec §2.3: "A conflict
   never drops the group or its files." Code: conflicts are never assigned
   to groups (`SetConflict` unassigns), and apply skips them
   (apply.go:132-140). This also contradicts mvp-gaps row 10 ("Conflict →
   still write files") — the code follows the *gaps table's* earlier
   stance, not the spec's. Needs a product decision, then alignment on
   both sides.
10. **NEW — fabricated capture time written to EXIF.** Spec §3.2:
    "Creation time is not a substitute for a missing capture time." Code
    falls back to birth/mtime (`identity.captureTime`), which is a
    documented pragmatic deviation — but `apply` then *writes* that
    fallback into `DateTimeOriginal` (apply.go:341-351) with a forced `Z`
    offset (`captureOffsetSec`, apply.go:375). EXIF-less JPEGs get an
    invented timestamp+timezone presented as original metadata. At minimum
    the provenance should be kept (e.g. `OffsetTimeOriginal` omitted when
    time is a fallback) — currently the `Z` fabricates timezone certainty
    the resolver explicitly refused to claim.
11. **NEW — invented grouping rules not in spec.** `captureTolerance=5s`
    birth-precedes-EXIF (group.go:19-24,166-171) and the
    near-identical-pixels+identical-instant rule (group.go:200-208) are
    good engineering (they drive 7 of the corpus's 8 original conflicts)
    but appear in no spec chapter; the latter directly trades against
    §2.3's "burst photos must not be grouped together".
12. **NEW — medoid / `hamming_to_medoid` absent.** Spec §4.5 wants the
    medoid image and per-member `hamming_to_medoid` in Group JSON. Not in
    the schema or output.
13. **NEW — `partition.New` (ExactSHA256 constructor) is dead code**; the
    pipeline always calls `NewDCT`. Symptom of mismatch #1.
14. **NEW — apply-time conflict classification by substring.**
    `strings.Contains(r.Conflict, "exif")` / "capture" / "hamming"
    (apply.go:186-196) re-derives conflict policy from a human-readable
    string that `ResolveGroup` produced. Conflict kinds should travel as
    a typed field on `Group`.
15. **NEW — banding edge case.** `banding.go:51-54` computes
    `bands = ceil(bitLen/2r)`; when `radius ≥ bitLen/2` this yields fewer
    bands than needed for the union bound (two hashes at distance ≤ r may
    share no band). With defaults (128 bits, r=12) it is correct; only
    extreme `--max-hamming` values degrade recall silently.
16. **NEW — O(n²) insertion sort in `Aggregate`** (group.go:92-105) for
    stable group IDs, and `Partition` re-scans the full library per
    unindexed file (partition.go:186) — both fine at corpus scale, both
    quadratic at library scale.
17. Stale spec status lines (spec says done/undone where code moved on):
    ch13 "no LSH/banding yet" (banding exists), ch04 "missing: banding,
    commit entities" (both exist), ch15 "within 1d" vs 10 s default,
    ch18 §9 "single source" vs multi-source code.
18. Gap-table drift: mvp-gaps row 14 says "apply refuses conflicts",
    row 10 says conflicts still write files — the code does *neither*
    consistently (§5.9/#14). Row 13's "suffix-only detection" is the
    honest one.

## 6. What's done well (keep)

- Test discipline: table-driven, boundary-focused (10 s/11 s, 5 s/6 s),
  hermetic fake clock, real-WASM engine used in tests — and tests caught
  the `sha256:` prefix bug in *both* sides of a mock pair simultaneously.
- The `Tx` helper, doc comments that state *why* (e.g. why the ledger is
  keyed by group id, why prefix never chains), and consistent naming.
- Layering is acyclic and mostly inward-pointing; `exif` isolation via
  `Resolver` is exactly right for an embed-size-sensitive CLI.
- Idempotent apply with an adoption path from `report.json` — the ledger
  design satisfies §2.4 ("repeated apply materialises nothing") with
  evidence in `docs/mvp-gaps.md`.

## 7. Suggested sequence (when coding resumes)

1. Typed conflicts on `Group` (kills #14, enables #9 decision) — small.
2. Plan-phase transaction + `ON CONFLICT` upserts (ACID gaps §4).
3. Exact-duplicate strategy wired into `Partition` (#1, #13) — closes the
   last untracked MVP gap; decide hardlink listing (#2) in the same pass.
4. Capture-fallback provenance in apply (#10) — small, protects data.
5. Decompositions §1.2–§1.4 opportunistically alongside feature work;
   §1.1 (cli flags) when the next flag is added.
6. Spec edits for #4/#17 (and the §4.5 "highest resolution" wording) in
   one docs commit.
