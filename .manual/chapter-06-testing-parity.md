# Chapter 06: Testing & parity

## Strategy (target)

- **`domain` is the parity target.** Port prototype `--self-test` vectors
  (capture-time incl. zoned/`+14:00`/`-12:00`, GPS 100 m boundary,
  pixel/name/encoding rank, incremental present/matched/new, rename swap)
  into Go table tests. Regression net for [chapter 02](chapter-02-semantics.md).
- **`hash`:** orientation invariance, Hamming symmetry, banding completeness
  (every true near-pair is a candidate).
- **`cluster` / merge:** golden tests on synthetic hashes — chains, and
  “matches more than one library group”.
- **`apply`:** temp-dir copy/move/rename-vacate; include a 3-cycle rename.
- **`store`:** commit atomicity + rollback on scratch DB.
- **E2E:** seed → commit → apply → merge second dir → commit → apply;
  assert T1–T4 across the merge.

## Migration / parity notes

- Python prototype was the oracle; keep its `--self-test` passing while
  porting. **That tree is gone from this repo** — treat chapter 02 + Go
  selftest as the living oracle; re-import vectors if the prototype returns.
- First Go build re-hashes the seed (§9.1) — no cache migration.
- Committed store (chapter 17) is new; prototype `work/` JSON
  (`unique.json`, `clusters.json`, `plan.json`, `phash.json`) maps 1:1 onto
  `files` / `pictures` / `picture_members` / `phash_cache` for an optional
  importer.

## What the MVP already runs

```text
plr selftest
go test ./internal/...
```

`internal/selftest` exercises deterministic conflict kinds (offset / GPS
disagreement). Birth-time-vs-capture is filesystem-dependent and lightly
covered. Grouping radius in selftest is **16** bits (CLI default **6**).

Package tests exist for: `phash`, `fingerprint`, `ingest`, `group`, `meta`,
`exifwrite`, `apply`, `store`.

## When changing semantics

1. Update [chapter 02](chapter-02-semantics.md) and the owning S-chapter.
2. Add / adjust table tests in the pure package (`group` today; `domain`
   when split).
3. Extend `internal/selftest` if the case is end-to-end visible.
4. Do not “fix” tests by relaxing T1–T4.
