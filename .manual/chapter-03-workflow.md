# Chapter 03: Stepwise workflow

**Current heading** (simple CLI). Old draft phases below are reference
only. Shortcuts: [`docs/mvp-gaps.md`](../docs/mvp-gaps.md),
[chapter 01](chapter-01-goals-and-status.md).

## Operator flow (today)

```text
plr scan  --dir PHOTOS [--force]     # ingest metadata into plr.db
plr group [--max-hamming N]          # crunch near-duplicates
plr plan                             # collision resolution → groups plan
plr apply --target RESULT            # materialize: copy + write EXIF
# or one-shot:
plr build --dir PHOTOS --target RESULT
plr increment --dir NEW_PHOTOS --target RESULT   # grow an existing library
```

`apply` and **materialize** mean the same step (CLI name stays `apply`).
Conflicts skipped on materialize unless `--force-conflicts`.

## Phase 1 — Build the seed (old draft)

```text
RegisterSource(kind=seed, path=PHOTOS)
Ingest(seed)            -> hash + dedupe exact copies
BuildSeed()             -> cluster (Hamming CC) + PlanGroup per cluster
                          -> working plan with conflicts surfaced
review Conflicts()      -> operator inspects capture/GPS conflicts
Commit()                -> atomic snapshot = library v1
Apply/materialize(dry_run=true) -> sanity check
Apply/materialize()      -> files land in RESULT/
```

**MVP today:** `plr build` or `scan` → `group` → `plan` → `apply`. No
RegisterSource kinds. `build` and `increment` commit the plan automatically
after the plan phase; `plr commit` / `plr rollback` are the manual commands.

## Phase 2 — Commit

Commit is the barrier between “planned” and “library”. Nothing in `RESULT/`
changes until apply/materialize, which only realizes a **committed** plan.

**MVP today:** plan rows live in `groups`; `plr commit` (and the automatic
commit after the plan phase in `build`/`increment`) snapshots them into
`commits` / `commit_groups` / `commit_actions` in one transaction, and
`plr rollback [--to N]` restores an earlier snapshot, recorded as a new
commit. Apply runs against the current plan rows in `groups`.

## Phase 3 — Merge external / backup directories

```text
RegisterSource(kind=backup, path=TM_BACKUP_2024)
Merge(backup)           ->
    1. hash incoming (cache-aware)
    2. dedupe incoming among themselves
    3. group incoming (Hamming CC, transitive)
    4. match each incoming picture vs library members
         present | join library group (re-aggregate) | new cluster
    5. PlanGroup over (existing members + joined members)
    6. plan actions: new / renamed / updated / untouched
Commit()                -> library v2 (diff vs v1 recorded)
Apply/materialize()      -> copy new files, rename/update changed ones
```

“Better” falls out of re-running `PlanGroup` over the union — larger
resolution wins pixels; capture/GPS/camera rules pick time/position/tags;
other tags fill gaps. Exact copies dropped at ingest; near-copies join an
existing picture.

**MVP today:** `plr increment` is the bypass as a command: `plr scan --dir NEW`
into same DB, then `group` / `plan` / `apply` in one go (loses
present/matched/new actions and true library-vs-inbox partition). New
photos that land in an already-registered directory need `--force`.

## Rebuild-from-backups

```text
for snap in sorted(backup_snapshots):
    RegisterSource(kind=backup, path=snap)
    Merge(snap)
    Commit()
Apply/materialize()   # once at the end
```

Invariants T1–T4 ([chapter 02 §2.6](chapter-02-semantics.md)) ensure a
later snapshot with wrong dates cannot move the timeline, while better
pixels or GPS still upgrade the picture. Commit log audits per-snapshot
contribution.
