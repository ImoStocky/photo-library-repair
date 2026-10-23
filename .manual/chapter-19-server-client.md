# Chapter 19: Server / Client (S9)

**Read when:** introducing gRPC/HTTP, job queue, or splitting CLI from
library work.

**Status:** Not implemented — MVP is a single local CLI.

## Roles (target)

- **Server** owns the library store and heavy work (scan, hash, cluster,
  EXIF, merge, apply). Stateful: DB + job queue.
- **Client** is thin: CLI (later web UI). Registers sources, starts jobs,
  streams progress, inspects plans/conflicts. Never touches photo files
  directly.

Default deploy: server + CLI on one host over localhost.

## Transport & API (target)

gRPC primary; HTTP/JSON + SSE facade for UI. Mutating RPCs return a **job
id** and run async.

```text
service Library {
  rpc RegisterSource(RegisterSourceReq) returns (Source);  // seed|backup|inbox
  rpc ListSources(Empty) returns (SourceList);

  rpc Ingest(IngestReq)   returns (Job);
  rpc BuildSeed(BuildReq) returns (Job);
  rpc Merge(MergeReq)     returns (Job);
  rpc Commit(CommitReq)   returns (Commit);
  rpc Apply(ApplyReq)     returns (Job);   // dry_run flag

  rpc Plan(PlanReq)       returns (Plan);
  rpc Pictures(ListReq)   returns (stream Picture);
  rpc Conflicts(PlanReq)  returns (ConflictList);
  rpc Job(JobID)          returns (Job);
}
```

## Jobs & progress

- Job = goroutine / small worker set + `context.Context` cancel.
- States: `queued → running → done | error | cancelled` + progress fraction
  + log tail (SSE / gRPC watch stream).
- Idempotent where possible (unchanged source + hash cache → no-op).

## Target packages

```text
cmd/plr-server/
cmd/plr-cli/
internal/server/   // handlers, job runner
internal/client/   // API client + CLI commands
```

## Current stand-in

`internal/cli` + `main.go` — synchronous phases against local SQLite.
No job IDs, no network, client *is* the worker.

## Handler sketch (Merge)

```go
func (sv *Server) Merge(ctx context.Context, req *MergeReq) (*Job, error) {
    return sv.jobs.Start("merge", func(ctx context.Context) error {
        // ingest.Scan → Dedupe → cluster → domain.Partition
        // → PlanGroup over unions → store.SetWorkingPlan
    }), nil
}
```

See also [chapter 03](chapter-03-workflow.md) for the human workflow these
RPCs encode.
