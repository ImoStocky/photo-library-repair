# Chapter 13: Cluster (S3)

**Read when:** changing near-duplicate grouping or medoid calculation.

**Spec:** [chapter 02 §2.2 Stage B](chapter-02-semantics.md).
**Decision:** [chapter 05 §9.2](chapter-05-decisions.md).
**Status:** Aligned with decision (Hamming CC); no banding; also unions
exact sha256.

## Responsibility

Connected components under Hamming ≤ `max_hamming`. Pure. Same relation
used for incremental matching ([chapter 16](chapter-16-merge.md)).

## Spec summary

- Edge iff Hamming ≤ `max_hamming` (transitive = one picture).
- Medoid = member with minimum sum of Hamming distances to others.
- Record `hamming_to_medoid`, cluster `max_hamming`.
- Unhashed / lone file = one-member picture.

## Target package

`internal/cluster` — uses `hash.Candidates` then exact Hamming filter +
union-find.

## Current package

`internal/group.Aggregate(imgs, maxHamming)`:

- Union-find over all pairs (O(n²)).
- Also unions identical non-empty `sha256`.
- Returns `imageID → groupID` and member slices.

No separate medoid fields on `groups` today (plan stores pixel/capture/gps
choices only).

## Pseudocode (target)

```go
func Cluster(recs []Member, maxHamming int) []Picture {
    parent := /* union-find */
    cand := hash.Candidates(hashesOf(recs), maxHamming)
    for each candidate pair (i, j):
        if Hamming(recs[i], recs[j]) <= maxHamming { union(i, j) }
    return pictures with medoid + members + MaxHamming
}
```

## CLI

`plr group [--max-hamming N]` — MVP default **12**, design default **32**.
