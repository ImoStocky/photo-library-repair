# Chapter 17: Store (S7)

**Read when:** changing SQLite schema, commit/rollback, or library entities.

**Design sections:** entities, commit model, schema (draft §5).
**Status:** Partial — index exists; commit model missing.

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
       gps_*, has_gps, group_id)
groups(id, status, reason, pixel_image_id, pixel_phash,
       capture_*, gps_*, stored_name, resolved_by)
```

No `commits`, `picture_members`, `phash_cache`, source `kind`/`label`.
Plan fields live on `groups`. IDs are time-based strings (`NewID`).

**MVP workaround:** `cp plr.db plr.db.bak` before risky phases.

## Pseudocode (target Commit)

```go
func (s *Store) Commit(ctx context.Context, plan []Picture) (Commit, error) {
    // resolveNameCollisions; markActions(new|renamed|updated|untouched)
    // tx: replacePictures + insertCommit + actions
}
```
