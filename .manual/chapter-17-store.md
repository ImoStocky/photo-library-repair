# Chapter 17: Store (S7)

**Read when:** changing SQLite schema, commit/rollback, or library entities.

**Design sections:** entities, commit model, schema (draft §5).
**Status:** Current schema fully implemented, including the commit model (commit log + rollback).

## Responsibility

Committed library index + commit log. I/O via `database/sql`.

## Target entities

```text
Source        { id, label, kind: seed|backup|inbox, path, added_at }
File          { id, source_id, path, size, mtime_ns, width, height,
                hash [16]byte, kind }
Picture       { id, stored_name, pixel_file_id, name_src,
                capture_time, capture_file_id,
                gps_lat, gps_lon, gps_file_id, camera_file_id,
                conflicts []string, max_hamming }
PictureMember { picture_id, file_id, role, hamming_to_medoid }
                // role ∈ pixel|name|capture|gps|camera|other
Commit        { id, at, summary }
CommitAction  { commit_id, picture_id, action: new|renamed|updated|untouched,
                prev_name, new_name }
```

`Picture` = canonical unit. `File` rows keep provenance (which backup).

## Commit model

- Working plan in memory / scratch until `Commit`.
- `Commit` = one DB transaction: replace picture index, append `Commit` +
  `CommitAction` rows; returns diff counts.
- `Apply` driven by **committed** plan only (`dry_run` supported).
- Rollback: re-apply an earlier commit as source of truth.

## Target schema

```sql
CREATE TABLE sources        (... kind TEXT, path TEXT, ...);
CREATE TABLE files          (... path TEXT UNIQUE, size, mtime_ns,
                             width, height, hash BLOB, kind TEXT);
CREATE INDEX idx_files_hash ON files(hash);
CREATE TABLE pictures       (... stored_name, pixel_file_id, ...,
                             conflicts TEXT, max_hamming INT);
CREATE TABLE picture_members(picture_id, file_id, role,
                             hamming_to_medoid, PRIMARY KEY(...));
CREATE TABLE commits        (id, at, summary);
CREATE TABLE commit_actions (commit_id, picture_id, action,
                             prev_name, new_name);
CREATE TABLE phash_cache    (path, size, mtime_ns, hash, width, height,
                             PRIMARY KEY(path, size, mtime_ns));
```

## Current schema (`internal/store`)

```text
sources(id, path UNIQUE, added_at)
images(id TEXT PK, source_id, rel_path, name, sha256, phash,
       created_at, exif_capture, exif_offset, width, height, kind, size,
       mtime, gps_*, has_gps, group_id)
groups(id, status, reason, pixel_image_id, pixel_phash,
       capture_*, gps_*, stored_name, resolved_by)
commits(id, at, summary)
commit_groups(commit_id, group_id, ...full groups row...,
              PRIMARY KEY(commit_id, group_id))
commit_actions(commit_id, group_id, action, prev_stored_name,
               new_stored_name)
```

No `picture_members`, no source `kind`/`label`; the phash cache is the
`images` columns (`mtime` plus the stored fingerprint). Plan fields live on
`groups`. IDs are time-based strings (`NewID`).

**Commit model (current schema, `internal/store/commit.go`):**
`Commit(summary)` snapshots the whole `groups` table into `commit_groups`,
records the commit row and the per-group diff (`new|updated|removed` versus
the previous commit) in `commit_actions`, all in one transaction.
`Rollback(id)` restores the `groups` rows of that snapshot, zeroes image
group assignments that point at groups the restore drops, and records the
restore itself as a new commit (`rollback to commit N`) so the log stays
append-only. `build` and `increment` commit automatically after the plan
phase; `plr commit` and `plr rollback` are the manual commands. Scanned
images are never touched.

## Pseudocode (target Commit)

```go
func (s *Store) Commit(ctx context.Context, plan []Picture) (Commit, error) {
    // resolveNameCollisions; markActions(new|renamed|updated|untouched)
    // tx: replacePictures + insertCommit + actions
}
```
