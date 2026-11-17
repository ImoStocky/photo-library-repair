# Next features plan — ETL trust / merge correctness / lineage

**Status:** draft for review (working tree already has uncommitted WIP matching most of Must/Should — re-verify before ship)  
**Branch target:** `devel` @ `e3ec631` + WIP  
**Scope:** Must + Should from the post-MVP roadmap. Nice/later stays listed only so review can push items in/out.  
**Ship shape:** one combined commit + push to `origin/devel` when you say go.

Direction: **trust before load → merge correctness → lineage/observability**.  
plr mapping: extract=`scan`, transform=`group`+`plan`, load=`apply` (filesystem materialize); SQLite=staging; sources read-only.

---

## Audit cross-check (ponytail review → this plan)

Residual audit Must/Should from that review were largely shipped in `e3ec631`
(`ReplacePlans`, inode `[2]uint64`, `DROP IF EXISTS plr_seen`,
`UpsertImageWithHashes`, vision reject, prepared `SetGroupAssignments`,
apply `*.tmp` cleanup, group/increment `DeletePlans`, README eval line,
CLI increment smoke). **This plan must not reopen those.**

| # | Audit finding | On `e3ec631` | Relation to this plan |
| --- | --- | --- | --- |
| 1 Must | Plan clear-then-rewrite window | **Fixed** — `ReplacePlans` one tx | Out of scope. Do not call `ClearGroups` up front in plan again. |
| 2 Must | Inode `Dev<<32\|Ino` collision | **Fixed** — `map[[2]uint64]` | Out of scope. |
| 3 Should | `plr_seen` TEMP sticky | **Fixed** — `DROP IF EXISTS` before create | Out of scope. |
| 4 Should | `ClearGroups` two statements | **Fixed** — one tx; plan uses `ReplacePlans` | Out of scope. |
| 5 Should | Image + content_phash split write | **Fixed** — `UpsertImageWithHashes` | Out of scope. |
| 6 Should | Fingerprint `ReadFile` whole file | **Fixed** (WIP) — stream `Open`+decode; JPEG meta re-reads after hash | Done; HEIC decoder may still buffer internally. |
| 7 Should | `--identity vision` silent stub | **Fixed** — rejected with error | Out of scope (VisionDecider still Nice/later). |
| 8 Should | `SetGroupAssignments` N UPDATEs | **Fixed** — prepared stmt | Out of scope (temp-table join still Nice). |
| 9 Nice | Apply leaves `*.tmp` | **Fixed** — glob cleanup at start | Out of scope. |
| 10 Nice | Kronk cloud dep tree | **Skip** (upstream) | Listed out of scope. |
| 11 Nice | `compare_groups.py` orphan | **Fixed** — README eval line | Out of scope. |
| 12 Nice | mvp-gaps selftest radius 16 vs code 20 | **Still stale** on HEAD (`maxHamming=20`, doc still says selftest 16) | Fold into **S2 docs sync** (one sentence). |
| 13 Nice | Only `build` CLI smoke | **Fixed** — increment CLI test in `e3ec631` | Out of scope; optional extra smoke for `--strict` when implementing M3. |
| 14 Nice | Re-group leaves stale plans | **Fixed** — `DeletePlans` after group/increment assign | Out of scope. M2 sticky ambiguity must survive that delete (separate `ambiguous_groups` table does). |
| 15 Nice | `AllImages` full load | **Skip** (OK at 200k) | Listed out of scope. |

**Verdict vs audit:** Plan scope is correct — new trust/merge/lineage work, not a second pass on shipped durability fixes. Only leftover from that review worth folding in now is **#12 doc radius** under S2. Optional promote: **#6** HEIC RAM (still Should, still open).

**WIP note (dirty tree, not committed):** M1–M4 + S1 largely present already (`ConflictKind*`, `ambiguous_groups`, `--strict`, `planFacts` skips capture on fallback, report lineage/stages). Kind id is `birth_before_exif` (not the draft name `birth_time_doubt`). Sticky store is table `ambiguous_groups`, not column `groups.ambiguous`. `MarkAmbiguousMerge` keeps an existing rule kind and appends the reason (matches open-question default #2).

---

## Out of scope (this cycle)

- Client/server, HDBSCAN, VisionDecider / `--identity vision`
- Kronk upstream (audit item 10)
- AllImages perf at 200k (audit item 15 — accepted OK)
- Audit items 1–5, 7–9, 11, 13–14 (already on `e3ec631`)
- Fingerprint whole-file RAM (audit #6) unless promoted
- Diameter clusters, magic-byte detect, hardlink listing, exact-dup Aggregate
- Persist band index, CLI flag unify, per-source watermark
- Re-planning S6 partition core (already implemented)
- Transferring Airflow / streaming / DW schemas

---

## Must (implement first)

### M1 — Typed conflict kinds

**Problem:** Conflicts are free-text `Reason` only. Apply and selftest re-derive policy from substrings / human strings (`design-review` §14).

**Change:**

| Layer | Change |
| --- | --- |
| `group.Conflict` / `group.Plan` | Add `Kind string` (stable machine id). Keep `Reason` as human text. |
| `store.Group` + `groups` / `commit_groups` | Add `conflict_kind TEXT NOT NULL DEFAULT ''` via `migrate` (same pattern as `pixel_phash`). |
| `SaveGroup` / `AllGroups` / commit snapshot | Round-trip `Kind`. |
| `ResolveGroup` | Set `Kind` when raising a conflict; resolver path keeps kind of the original conflict (or clears on settle — prefer **keep kind, status→resolved**, so lineage still shows what was settled). |
| Apply / report / show | Prefer `Kind` for policy; keep printing `Reason`. Stop any substring policy if present. |

**Kinds (closed set for this slice):**

| Kind | Raised by | Current reason text (keep wording unless tests force a tweak) |
| --- | --- | --- |
| `undecodable` | `ResolveGroup` singleton undecodable + no EXIF | `picture cannot be decoded and carries no EXIF capture time` |
| `birth_before_exif` | `birthTimeDoubt` | existing birth/capture complaint (WIP const name; not `birth_time_doubt`) |
| `gps` | `gpsMedoid` beyond tolerance | existing GPS complaint |
| `ambiguous_merge` | increment path (M2) | `matches more than one library group` (or equivalent) |

`shortcut: first conflict wins when multiple fire in ResolveGroup (today's reason overwrite order); upgrade when multi-kind conflicts are needed.`

**Acceptance:**

- Plan/conflict rows have non-empty `conflict_kind` whenever `status=conflict`.
- Selftest / unit tests assert on `Kind`, not only `strings.Contains(Reason, …)`.
- Existing libraries without the column migrate cleanly.

### M2 — Ambiguity → plan conflict (`ambiguous_merge`)

**Problem:** `Partition` sets `Match.Ambiguous` and joins lowest group id; CLI only prints a warning count. Spec (ch16 / mvp-gaps row 1) still defers “marking ambiguity as a plan conflict”.

**Change:**

1. Keep join behavior (lowest group id) — do **not** merge library groups.
2. After assign, for each ambiguous present/matched image, mark the **joined library group** so plan will conflict:
   - Smallest complete path: collect `map[int64]struct{}` of ambiguous target group ids during the increment loop; after `DeletePlans` / before or as part of plan phase, either:
     - **A (preferred):** stash ids on the store or return them from `runIncrementGroup` and have `runPlanPhase` force `Kind=ambiguous_merge` when resolving those groups (even if rules would otherwise resolve), **or**
     - **B:** write a sticky flag on the group row at assign time (new column `ambiguous INTEGER`) that `runPlanPhase` reads.
3. Prefer **A without a new column** if plan always re-runs after increment in the same process: pass `ambiguousGroups []int64` into plan for that pipeline run. For `plr plan` alone after a crash, ambiguity is lost unless we persist — so **persist a sticky bit** if we care about `increment` then later standalone `plan`.

**Decision for review (default proposal):** persist sticky ids in a separate `ambiguous_groups(group_id)` table (WIP choice — survives `DeletePlans` / `ReplacePlans` without a `groups.ambiguous` column). Recompute each increment from Partition (`ReplaceAmbiguousGroups`). Plan reads it and OR’s into conflict as `ambiguous_merge` (if already a rule conflict, keep rule kind and append reason; if only ambiguous, kind=`ambiguous_merge`). Clear after successful plan write.

**CLI:** keep the warning line; additionally those groups show as conflicts in `plr show` / plan counts.

**Acceptance:**

- Fixture with equal distance to two library groups → joined lowest id **and** that group’s plan `status=conflict`, `kind=ambiguous_merge`.
- mvp-gaps row 1 / ch16: remove “Deferred: marking ambiguity as a plan conflict”.

### M3 — `--strict` refuse unresolved conflicts (default off)

**Problem:** Apply always best-effort copies conflict groups. ETL-trust wants an explicit pre-load gate.

**Change:**

| Surface | Behavior |
| --- | --- |
| `apply.Options.Strict bool` | When true: if any group has `status=conflict`, return error **before** writing files / updating ledger / rewriting report. Message lists count + kinds. |
| `plr apply --strict` | Wire flag (default false). |
| `plr build` / `plr increment` | Same flag on shared pipeline options; apply phase respects it. |
| Dry-run + strict | Still refuse (gate is on plan state, not on I/O). |

Non-strict behavior unchanged (plain copy + ledger `conflict` reason).

**Acceptance:**

- Library with one conflict + `--strict` → exit ≠ 0, target untouched.
- Same without `--strict` → current best-effort apply.
- Docs: one line in defaults / apply help.

### M4 — Capture-fallback: do not invent DateTimeOriginal

**Problem:** `Plan.CaptureFallback` is already set when no EXIF capture exists (`pickCapture`). `planFacts` still builds Capture/Offset from `CaptureTime`/`CaptureInstant` (file birth fallback), so apply can write an invented `DateTimeOriginal`.

**Change:**

```go
// planFacts: if g.CaptureFallback { do not set Capture/Offset; GPS-only facts still OK if HasGPS }
```

- If fallback and no GPS and jpeg: treat as no EXIF write (same as today when offset parse fails), **or** write GPS-only when present.
- Do **not** clear `CaptureTime` on the plan (display/lineage still show fallback wall clock); only omit authoritative EXIF capture fields on materialize.
- `setCaptureTimes` / filesystem mtime: leave as today unless tests show we stamp invented capture onto mtime in a misleading way — call out in review if we should skip mtime stamp on fallback too.

**Acceptance:**

- Group with no EXIF capture → output JPEG has no new DateTimeOriginal from plr (source EXIF unchanged / absent).
- Group with real EXIF capture → unchanged merge write.
- Unit test on `planFacts` + one apply test.

---

## Should (same commit if still small)

### S1 — Additive `report.json` lineage + stage counters

Extend `reportJSON` / `groupJSON` **additively** (old readers ignore new fields):

| Field | Meaning |
| --- | --- |
| `groups[].conflict_kind` | From store |
| `groups[].pixel_image_id` / `capture_image_id` | Lineage ids |
| `groups[].resolved_by` | rules / model id |
| `stage` (top-level object) | Optional counters passed in from CLI when known: `scanned`, `grouped`, `planned`, `conflicts`, `ambiguous`, `applied_*` — only fill what the calling phase knows; omit zeros with `omitempty` |

Do **not** invent a second report format. Increment smoke still only needs exit + file presence.

**Acceptance:** after apply, `report.json` includes `conflict_kind` on conflict groups and pixel/capture ids on resolved groups.

### S2 — Docs sync

| Doc | Edit |
| --- | --- |
| `docs/mvp-gaps.md` row 1 | Ambiguity-as-conflict implemented; keep warning+lowest-id behavior noted. |
| `docs/mvp-gaps.md` notes | **Audit #12:** selftest radius is **20** (same as CLI default), not 16. |
| `.manual/chapter-16-merge.md` | Status: ambiguity plan conflicts landed; describe kind + `ambiguous_groups` sticky table. |
| `.manual/chapter-01-goals-and-status.md` | Fix “S6 … remain out of scope” → S6 core + ambiguity conflicts done. |
| `docs/perf-increment-plan.md` | Mark P0–P3 / P5 landed (P4 scan mid-walk already; persist band index still later). |
| `docs/defaults.md` / README help | `--strict` default off. |
| `docs/design-review.md` §14 | Note typed kind landed (optional one-liner; don’t rewrite the whole review). |

---

## Nice / later (not in this slice unless you promote)

- Diameter clusters, magic-byte detect, hardlink listing
- Exact-dup Aggregate, persist band index
- VisionDecider, CLI flag unify, per-source watermark
- Multi-kind conflicts on one group
- Strict mode that still materializes resolved groups and skips only conflicts (current Strict = all-or-nothing gate)

---

## Touch list (expected files)

**Go**

- `internal/group/group.go` — Kind on Plan/Conflict; ResolveGroup sets kinds
- `internal/group/*_test.go` — kind + capture-fallback related if any
- `internal/group/partition.go` — unchanged behavior; Ambiguous already exists
- `internal/store/store.go` (+ `commit.go` if snapshot columns) — `conflict_kind`, maybe `ambiguous`
- `internal/store/store_test.go`
- `internal/cli/cli.go` — ambiguous sticky, `--strict`, stage counters into apply report if easy
- `internal/apply/apply.go` — Strict gate, planFacts fallback, report fields
- `internal/apply/apply_test.go`
- `internal/selftest/selftest.go` — assert kinds where reasons are checked today

**Docs**

- `docs/mvp-gaps.md`, `docs/defaults.md`, `docs/perf-increment-plan.md`
- `.manual/chapter-01-goals-and-status.md`, `.manual/chapter-16-merge.md`
- this file → mark Implemented when shipped

---

## Test plan

1. `go test ./internal/group/... ./internal/store/... ./internal/apply/... ./internal/cli/...`
2. Focused cases:
   - ResolveGroup kinds for undecodable / gps / birth_time_doubt
   - Ambiguous partition → plan conflict `ambiguous_merge`
   - `Apply` strict vs non-strict
   - `planFacts` / apply: CaptureFallback does not write DateTimeOriginal
   - report.json additive fields present
3. `go vet` + `staticcheck` on touched packages; `gofmt -s -w`
4. `plr selftest` if apply/plan checks need kind updates
5. `graphify update .` after Go edits

---

## Open questions for review

1. **Ambiguity persistence:** sticky `ambiguous_groups` table (**default / WIP**) vs in-process-only handoff vs `groups.ambiguous` column?
2. **Ambiguity + other conflict:** keep rule kind and append ambiguity to reason (**default / WIP**), or always prefer `ambiguous_merge`?
3. **Strict semantics:** fail entire apply if any conflict (**default / WIP**) vs skip-conflict-only materialize?
4. **Fallback mtime:** skip `setCaptureTimes` when `CaptureFallback`? **Default: leave mtime unchanged this slice; EXIF-only fix.**
5. Promote audit **#6** (fingerprint RAM) or any Nice item into this commit? **Default: no.**

---

## Implementation order (when approved)

1. M1 kinds (schema + ResolveGroup + tests)
2. M2 ambiguity → conflict (sticky + plan)
3. M4 capture-fallback in `planFacts` (+ test)
4. M3 `--strict`
5. S1 report lineage/counters
6. S2 docs
7. format / vet / staticcheck / tests / selftest / graphify update
8. One commit + push on request
