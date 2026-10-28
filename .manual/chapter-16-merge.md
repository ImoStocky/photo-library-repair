# Chapter 16: Merge / incremental partition (S6)

**Read when:** implementing or changing `increment` / library merge.

**Spec:** [chapter 02 §2.5](chapter-02-semantics.md).
**Workflow:** [chapter 03 Phase 3](chapter-03-workflow.md).
**Status:** Core implemented (2026-10-27); S7 action marking implemented at
apply time via the apply ledger (2026-10-28); ambiguity plan conflicts still
deferred.

## Responsibility

Partition incoming connected components against library members:
`present` / `matched` (join + re-aggregate) / `new`. Pure.

## Target package

`internal/group.Partition` (+ action marking with store in S7).

## Current state

`plr increment` scans the new dirs, groups the incoming images among
themselves, refines per `--identity`, then partitions them against the
library (chapter-16 match table): present and matched members join their
library group, unmatched members become new groups. Ties across groups
join the lowest id and print an ambiguity warning instead of merging the
library groups. It does **not** yet emit ambiguity conflicts in the plan.

The present/matched/new *actions* and name-collision prefixes are realized
at apply time: apply keeps a `group_outputs` ledger per target directory
and reports `new` / `updated` / `renamed` / `untouched` per group (see
below and [mvp-gaps row 14](../docs/mvp-gaps.md)).

## Match table (distance to any library member)

| Closest | Result |
| --- | --- |
| 0 + same size | `present` |
| 0 different size, or 1…`max_hamming` | join that library group; re-`PlanGroup` |
| >`max_hamming` | new cluster |

Tie across library groups: smaller distance wins; equal → lower group id +
`"matches more than one library group"`.

Pre-steps: hash → dedupe among incoming → group incoming among themselves
(Hamming CC) **before** library compare.

## Pseudocode (target)

```go
func Partition(components [][]Member, lib []libMember, maxHamming int) (
    present, matched []Member, unmatched [][]Member) {
    // for each component:
    //   exact same-size hits -> present
    //   remaining: if min dist > maxHamming -> unmatched
    //   else join chosen library group (ambiguity flag if multi-group)
}
```

## Actions after re-aggregate

| Action | Meaning |
| --- | --- |
| `new` | Not in library; apply copies in |
| `renamed` | Better name; apply renames library file |
| `updated` | Same name; pixels or meta change |
| `untouched` | Nothing to write |

Name collision: prefix with group number (`000522_IMG_0007.jpg`).
