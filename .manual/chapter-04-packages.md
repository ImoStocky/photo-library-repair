# Chapter 04: Go package layout

## Target layout (design)

```text
go.mod  (module plr)

cmd/
  plr-server/            // main: start store, jobs, gRPC/HTTP
  plr-cli/               // main: thin client over the API

internal/
  domain/                // S5 + S6: PlanGroup, partition — PURE
  hash/                  // S2: DCT phash, Hamming, banding
  ingest/                // S1: walk, sniff, dedupe
  cluster/               // S3: connected components
  meta/                  // S4: exiftool stay_open, batch R/W
  store/                 // S7: SQLite, commit tx
  apply/                 // S8: materialize, rename-vacate, report
  server/                // S9: gRPC/HTTP, job runner
  client/                // S9: API client + CLI commands
```

`domain` imports nothing from `meta` / `store` / `server`; it takes plain
structs and a `Meta` lookup so the spec core stays testable.

## Actual layout (MVP)

```text
go.mod  (module photo-library-repair)

main.go                  // os.Exit(cli.Run(...))

internal/
  cli/                   // all commands (scan/group/plan/apply/build/report/selftest)
  ingest/                // S1 scan
  phash/                 // S2 DCT (was design's hash/)
  fingerprint/           // decode + orient + call phash + EXIF facts
  group/                 // S3 Aggregate + S5 ResolveGroup (combined)
  meta/                  // pure-Go JPEG EXIF read
  exifwrite/             // pure-Go JPEG EXIF write
  store/                 // SQLite sources/images/groups
  apply/                 // copy + write EXIF
  kronk/                 // Kronk decision-model resolver (plan --ai)
  selftest/              // built-in parity checks
  testimg/               // synthetic JPEG helpers for tests
```

Missing vs target: `domain/`, `cluster/` (logic in `group`), `server/`,
`client/`, `cmd/plr-*`, banding, commit entities.

## Subsystem → chapter map

| # | Subsystem | Chapter | Pure? |
| --- | --- | --- | --- |
| S1 | Ingest | [11](chapter-11-ingest.md) | I/O + pure dedupe |
| S2 | Hash | [12](chapter-12-hash.md) | yes |
| S3 | Cluster | [13](chapter-13-cluster.md) | yes |
| S4 | Meta | [14](chapter-14-meta.md) | I/O |
| S5 | Aggregate | [15](chapter-15-aggregate.md) | **yes — spec core** |
| S6 | Merge | [16](chapter-16-merge.md) | yes |
| S7 | Store | [17](chapter-17-store.md) | I/O |
| S8 | Apply | [18](chapter-18-apply.md) | I/O |
| S9 | Server/Client | [19](chapter-19-server-client.md) | I/O |
