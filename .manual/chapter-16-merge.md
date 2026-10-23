# Chapter 16: Merge / incremental partition (S6)

**Read when:** implementing or changing `increment` / library merge.

**Spec:** [chapter 02 §2.5](chapter-02-semantics.md).
**Workflow:** [chapter 03 Phase 3](chapter-03-workflow.md).
**Status:** Not implemented.

## Responsibility

Partition incoming connected components against library members:
`present` / `matched` (join + re-aggregate) / `new`. Pure.

## Target package

`internal/domain.Partition` (+ action marking with store in S7).

## Current state

No `plr increment`. Bypass (lossy): scan new dir into same DB → regroup →
replan → apply. That does **not** emit present/matched/new actions, name
collision prefixes, or ambiguity conflicts.

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
