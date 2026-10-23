# Photo Library Repair — design chapters

**Current heading:** local CLI — ingest metadata, crunch groups, resolve
collisions, amalgamate on demand, grow the SQLite library incrementally.
Start at [chapter 01](chapter-01-goals-and-status.md).

**apply ≡ amalgamate:** product verb is *amalgamate*; CLI/package stay
`apply` until an explicit rename. See [chapter 18](chapter-18-apply.md).

Older client/server draft split lives below for reference (not the
target). Full text: [`_archive/go-design-draft.md`](_archive/go-design-draft.md).

| Read when… | Chapter |
| --- | --- |
| Orienting; current MVP vs heading | [01 — Goals & status](chapter-01-goals-and-status.md) |
| Grouping / conflict / merge rules (draft ref) | [02 — Semantics](chapter-02-semantics.md) |
| End-to-end seed / merge / rebuild flow | [03 — Workflow](chapter-03-workflow.md) |
| Adding packages or moving code | [04 — Packages](chapter-04-packages.md) |
| Choosing libraries / engines | [05 — Decisions](chapter-05-decisions.md) |
| Writing tests or parity work | [06 — Testing & parity](chapter-06-testing-parity.md) |
| Scan / walk / exact de-dupe | [11 — Ingest (S1)](chapter-11-ingest.md) |
| Phash / Hamming / banding | [12 — Hash (S2)](chapter-12-hash.md) |
| Near-duplicate clustering | [13 — Cluster (S3)](chapter-13-cluster.md) |
| EXIF read / write engine | [14 — Meta (S4)](chapter-14-meta.md) |
| Plan / collision resolution | [15 — Aggregate (S5)](chapter-15-aggregate.md) |
| Incremental merge into a library | [16 — Merge (S6)](chapter-16-merge.md) |
| SQLite schema / commit model | [17 — Store (S7)](chapter-17-store.md) |
| Materialize (`apply` / amalgamate) | [18 — Apply / amalgamate](chapter-18-apply.md) |
| gRPC / jobs / thin CLI (old draft) | [19 — Server/Client (S9)](chapter-19-server-client.md) |

[`chapter-20-developer-guide.md`](chapter-20-developer-guide.md) is Kronk
SDK material parked here — **not** this product's guide. Prefer
chapter 01 + the S-chapters above.

Operational MVP bypasses: [`../docs/mvp-gaps.md`](../docs/mvp-gaps.md).
