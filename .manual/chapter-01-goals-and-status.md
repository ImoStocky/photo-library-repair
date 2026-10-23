# Chapter 01: Goals & current status

Read this first when picking up the project.

The **product heading** is the simpler Go tool from the OpenCode
“Library fix tool in go” brief — not the archived client/server draft.
[Chapters 02–06](chapter-02-semantics.md) retain the draft's semantics and
decisions — chapter 02 is the authoritative rule spec and the S-chapters
track each package. The draft's *architecture* (client/server, commit
model) is not the current target.

## Goals (current heading)

1. **Ingest metadata** — walk directories into a SQLite library (`scan`).
   Already-scanned dirs are skipped unless `--force`.
2. **Crunch** — exact `sha256` + perceptual-hash Hamming groups (`group`).
3. **Resolve collisions** — best pixels + earliest capture / GPS; hard
   conflicts left for later human/model review (`plan`).
4. **Amalgamate on demand** — copy planned images into a target dir and
   write merged EXIF onto the copies. Sources and the DB stay put.
5. **Grow the library incrementally** — scan more dirs into the same DB,
   re-group / re-plan, amalgamate again (`increment`).

One-shot: `build` = scan → group → plan → amalgamate.
Incremental: `increment` = the same pipeline into an existing library.

## Naming: `apply` and `amalgamate`

| Term | Meaning |
| --- | --- |
| **amalgamate** | Product verb: materialize the plan into `--target` |
| **apply** | Current CLI / package name for the same step |

**Do not rename the CLI yet** (other agents may be mid-flight). Document
both; code and flags stay `plr apply` / `internal/apply` until an
explicit rename pass.

`build` = full pipeline. It is not a synonym of amalgamate.

## Non-goals (for now)

- No client/server / gRPC / commit-log store (old draft).
- No image editing as the core path (optional Kronk upscale later).
- No multi-user ACL / face / scene ML in the core pipeline.

## What exists today (MVP)

```text
plr scan | group | plan | apply | build | increment | report | selftest
```

(`apply` ≡ amalgamate; see above.)

SQLite `plr.db`: `sources` / `images` / `groups`. Local CLI only.

| Step | Owner | Status |
| --- | --- | --- |
| Ingest | `internal/ingest` | Done — skip scanned dirs unless `--force` |
| Hash | `internal/phash`, `fingerprint` | Done — 16-byte DCT phash (czkawka-style) |
| Group | `internal/group.Aggregate` | Done — sha256 + Hamming CC |
| Plan / conflicts | `internal/group.ResolveGroup` | Done — rules per phase-5 brief; `Resolver` stub for Kronk |
| Amalgamate / apply | `internal/apply` | Done — copy + JPEG EXIF; no `--move` |
| One-shot | `plr build` | Done |
| Incremental growth | `plr increment` | Done — scan + group + plan + apply into the existing DB; S6 partition actions (present/matched/new) remain out of scope |
| Kronk conflict resolve | `internal/kronk` | Done — opt-in `plr plan --ai` (Kronk decision model); rules remain the default |

## Related

- Command materialize details: [chapter 18](chapter-18-apply.md)
- MVP bypasses: [`docs/mvp-gaps.md`](../docs/mvp-gaps.md)
- Old heavy design (archive): [`_archive/go-design-draft.md`](_archive/go-design-draft.md)
