# Photo Library Repair — Go Client/Server Design

Status: **target design**. The Python prototype in this repo is the reference
implementation. This document (a) *retains* the exact grouping and conflict-merge
semantics the prototype implements, and (b) decomposes the system into a Go
client/server application with a committed, versioned library store.

Ground rules for this document:

- The Python code is **not** modified. It is the behavioral oracle.
- Section 2 is the **authoritative spec**. The Go implementation MUST reproduce
  it. Where the Go design deliberately diverges (clustering engine, phash
  library), the divergence is called out in Section 9 and is behavior-preserving.
- Pseudo code is Go-flavored and may rely on Go capabilities (goroutines,
  channels, `sync`, `context`, `database/sql`, `net`, stdlib `image/*`).

---

## 1. Goals and non-goals

### Goals

1. **Build a seed library** from one or more directories: hash, de-duplicate,
   group near-duplicates, and resolve internal metadata conflicts into one
   canonical picture per group.
2. **Commit** the resolved library into a durable, versioned store ("the
   library"). A commit is an atomic, auditable snapshot of the picture index.
3. **Merge external directories** (Time Machine backups, other photo folders,
   an inbox) into the committed library. Incoming files are pre-processed
   (hashed, grouped among themselves), matched against the library, and folded
   in — keeping the library de-duplicated. Matches may contribute **better
   pixels, better capture time, a GPS position, or more descriptive metadata**.
4. **Rebuild the library from backups** by repeatedly running step 3 against
   successive backup snapshots.
5. **Preserve the timeline.** The stored capture time of a picture must not be
   shuffled by merging a duplicate whose file dates or clock are wrong.

### Non-goals (for now)

- No image editing / pixel repair. "Repair" means metadata + selection, not
  content restoration.
- No multi-user concurrency / access control. Single operator, one library.
- No face detection, scene understanding, or ML embedding (the UMAP path in the
  prototype is a separate inspection tool, not part of the core pipeline).

---

## 2. Retained semantics (authoritative spec)

This section is a faithful capture of the prototype's behavior. Constants are
exactly as in the code.

### 2.1 Identity

- The **identity of a picture is its 256-bit perceptual hash** (a DCT phash,
  `hash_size=16` → 16×16 = 256 bits), computed **after EXIF orientation** is
  applied (so a sideways-stored photo hashes like its upright display).
- Distance between two pictures is the **Hamming distance** between their 256-bit
  hashes.
- Default merge radius: **`max_hamming = 32` bits**.
- JPEG is decoded as a reduced preview (edge ≈ 64 px) before hashing for speed;
  the *stored* width/height remain the full displayed size (orientation-aware).
  Other formats are decoded in full, then oriented the same way.
- A hash is cached by **(path, size, mtime_ns)** so unchanged files are not
  re-decoded.

### 2.2 Grouping

Two stages, both retained.

**Stage A — exact-copy de-duplication (during collection).**

- Walk each source directory recursively. A file is an image when its **magic
  bytes** (not the filename suffix) identify JPEG, PNG, GIF, WebP, TIFF, or
  HEIC.
- `--min-size` (KB) drops images smaller than the threshold (default 0 = keep
  all).
- A resolved path is visited once; a **hard link** to the same inode is decoded
  once but still listed.
- Grouping key for de-dup: **same phash AND same file size** → the same
  displayed picture. Keep the **earliest** by `(mtime, path)`; later copies are
  **dropped** (they stay on disk and are recorded, but never clustered or
  applied).
- **Different file size with the same phash is kept** — aggregation may still
  prefer the larger pixels.
- Files whose hash fails are kept as their own single-picture group (never lost).

**Stage B — near-duplicate clustering.**

- Prototype default: HDBSCAN (or OPTICS) over the 256-bit hashes with Hamming
  metric, then a **post-pass that splits any cluster into connected components
  where an edge exists only if Hamming ≤ `max_hamming`**. Noise points become
  their own cluster. A single image is a normal cluster.
- Each cluster records a **medoid** (the member with the minimum sum of Hamming
  distances to the others), each member's `hamming_to_medoid`, and the cluster's
  `max_hamming`.

> **Go decision (Section 9.2):** the Go implementation uses the *post-pass rule
> directly* as the clustering engine — connected components under
> "Hamming ≤ `max_hamming`", with locality-sensitive banding for scale. This is
> the same relation the prototype applies *after* HDBSCAN, it is deterministic
> and dependency-free, and it is **identical to the rule already used for
> incremental matching** (Section 2.5), so seed and merge share one grouping
> definition.

### 2.3 Conflict merge (the per-picture plan)

For each group, **one** canonical picture is produced. Nothing is moved at this
stage; a plan is emitted. Members are treated as "the same picture". The chosen
fields:

**Pixels — `choosePixel`.** Largest `width × height`. Tie → better encoding by
rank **HEIC(5) > JPEG(4) > TIFF(3) > PNG(2) > WebP(1) > GIF(0)**. Tie → larger
file size. This file is the one materialized (copied, or moved with `--move`).

**Name — `chooseName`.** Shortest basename. Tie → alphabetically earlier
(case-folded). The output **extension is taken from the pixel file's bytes**
(sniffed kind) so the metadata writer can write it; if the chosen name's
extension doesn't match the pixel bytes' kind, the pixel's canonical extension
is substituted.

**Capture time.** The **only** capture clock is EXIF `DateTimeOriginal`.
`CreateDate`, XMP dates, IPTC dates, and file creation time are **not** capture
times. The stored value is the **local wall clock** `YYYY:MM:DD HH:MM:SS`.

- `OffsetTimeOriginal` is applied when comparing two `DateTimeOriginal` values.
  An offset written on the timestamp itself (`+10:00`, `+1000`, or `Z`) is used
  too. If one file has a zone and the other only a bare clock, the two **wall
  clocks** are compared.
- "Camera data" = any of: make, model, lens make/model, lens info, focal length,
  focal length (35 mm), F-number, exposure time, ISO, exposure program, exposure
  compensation, flash, metering mode, white balance, serial number. A file with
  any of these is a **camera file** and is assumed to hold the right capture
  time.
- `max_capture_disagreement` (default **`1d` = 86400 s**; `12h`, `90s`, or a bare
  number of seconds are accepted) is the allowed gap.

Rules:

| Situation | Result |
| --- | --- |
| ≥ 2 camera files, each with `DateTimeOriginal` | Compare their capture times. Gap > `max_capture_disagreement` → conflict **"capture times disagree"**. Within the limit → keep the **oldest**, no conflict. A later time on a *non-camera* file does not enter this comparison. |
| No camera file at all | Keep the **earliest** `DateTimeOriginal`. Gap size is irrelevant; **no** conflict. |
| Camera data present, but none of those files has `DateTimeOriginal` | **No** capture time is copied. Creation time is **not** used as a substitute. |

Camera tags are copied from the file that supplied the chosen camera capture
time. If no camera file has `DateTimeOriginal`, tags come from the camera file
with the **most** camera fields.

**GPS.** A position is read from GPS latitude/longitude. `(0,0)` is ignored. A
file with no position does not vote.

| Situation | Result |
| --- | --- |
| Any two positions **> 100 m** apart | Conflict **"gps positions disagree by more than 100 m"**; **no** GPS is copied. |
| All positions within 100 m | Copy the **smallest** coordinates: smallest latitude, then smallest longitude. |

**Other tags.** Description, artist, copyright, keywords, rating, title, subject,
etc. are **copied from the other members** (filled in where the pixel file lacks
them) and are **not** conflict checks.

**A conflict never drops the group.** The group is still emitted and its pixel
file is still materialized. `conflicts` is recorded on the plan and later on the
result report.

### 2.4 Materialize (apply)

- Copy the chosen pixel source into the result directory under the planned name,
  then **write** the chosen capture time, GPS, and camera tags onto that copy
  with the metadata writer (ExifTool). The source file stays in place.
- `--move` moves the pixel source instead (it then leaves the source directory).
  Other cluster members and dropped exact copies stay where they are either way.
- A **new** file refuses to overwrite a name already present in the result
  directory. An **update** overwrites. A **rename** moves the existing library
  file to its new name.
- The result report lists, per picture: output path, source files, and conflicts.

### 2.5 Incremental merge (adding to an existing library)

The library work state (hash cache, cluster index, plan) is **read-only** during
a merge. Steps:

1. **Hash the incoming files once** (cache-aware against the library cache).
2. **De-duplicate among themselves**: distance 0 + same size keeps the earliest;
   later copies are dropped.
3. **Group incoming among themselves**: two hashes within `max_hamming`,
   including a **chain** of such pairs, are one incoming picture (transitive
   connected components). This happens *before* the library comparison, so
   near-copies in the inbox become one picture even if one is close to the
   library and another is farther.
4. **Match each incoming picture against the library** — distance is to **all
   library members**, not just the medoid:

   | Closest distance (picture → library member) | Result |
   | --- | --- |
   | **0 and same file size** | That file is **present** (already in the library). |
   | **0 at a different size, or 1 … `max_hamming`** | The **whole** incoming picture **joins that library group** and the group is re-aggregated. Exact copies in the picture stay `present`. |
   | **> `max_hamming`** | The picture is **new**; its files stay together as one new cluster with a fresh group id above the highest library id. |

   If two library groups are both close enough, the picture joins the **smaller
   distance**. Equal distances join the **lower group id** and the picture is
   flagged **"matches more than one library group"** (an ambiguity conflict).

5. **Plan actions** per picture:

   | Action | Meaning |
   | --- | --- |
   | `new` | Not in the library. Materialize copies it in. |
   | `renamed` | In the library and a better name was found. Materialize renames the library file. |
   | `updated` | Name stays; pixels or metadata change. Materialize overwrites that file. |
   | `untouched` | Nothing to write. |

6. **Name collision**: if a new/renamed picture wants a name another library
   picture already has, its name is prefixed with its group number
   (e.g. `000522_IMG_0007.jpg`). The original file keeps its name.
7. The finished plan, cluster index, and hash cache are written **back into the
   library store**, so the next merge sees the extended library.

### 2.6 Timeline invariants

These are the properties that keep the timeline from being shuffled, and they are
load-bearing for the rebuild-from-backups workflow:

- **T1.** A picture's stored capture time is a **pure function** of its members'
  `DateTimeOriginal` + camera data, chosen deterministically by the rules in
  2.3. It does not depend on file mtime, path, or arrival order.
- **T2.** Merging a duplicate **never changes** the stored capture time unless a
  camera-data member disagrees beyond `max_capture_disagreement`; in that case it
  is recorded as a conflict and the **oldest** camera time is still kept.
- **T3.** Only `DateTimeOriginal` counts. A backup copy whose `CreateDate`/mtime
  is wrong, or whose camera clock was reset, cannot move the picture on the
  timeline.
- **T4.** "Keep the oldest within the limit" means a set of consistent camera
  copies always collapses to the single earliest genuine capture instant.

---

## 3. Problem decomposition

The system decomposes into nine subsystems. The first six are **pure or
nearly-pure** and are the ones that must match Section 2 exactly; the last three
are the new client/server machinery.

| # | Subsystem | Responsibility | Pure? |
| --- | --- | --- | --- |
| S1 | **Ingest** | Walk dirs, sniff kind, stat, de-duplicate exact copies | yes (I/O + pure dedupe) |
| S2 | **Hash** | Orient + DCT phash (256-bit), Hamming, banding index | yes |
| S3 | **Cluster** | Connected components under Hamming ≤ `max_hamming` | yes |
| S4 | **Meta read** | Batch EXIF read (capture time, GPS, camera, other) | I/O (exiftool) |
| S5 | **Aggregate** | `PlanGroup`: pixels/name/capture/GPS/camera + conflicts | **yes — the spec core** |
| S6 | **Merge** | Incremental partition (present / matched / new) | yes |
| S7 | **Store** | Committed library index + commit log (SQLite) | I/O |
| S8 | **Apply** | Materialize (copy/move/rename-order) + write meta + report | I/O |
| S9 | **Server/Client** | API, async jobs, progress, CLI/UI | I/O |

Dependency rule: S5 and S6 depend only on S2's types and on plain data (no I/O),
so they are trivially unit-testable against the prototype's self-test vectors.

---

## 4. Client-server architecture

### 4.1 Roles

- **Server** owns the library store and all heavy work (scan, hash, cluster,
  EXIF, merge, apply). It is stateful: the library DB + a job queue.
- **Client** is thin: a CLI (and later a web UI). It registers sources, starts
  jobs, polls/stream progress, and inspects plans/conflicts. It never touches
  photo files directly.

A single-host deployment (server + CLI on the same box, talking over
localhost) is the default; the split still lets the UI run elsewhere and lets
long jobs survive a closed terminal.

### 4.2 Transport and API

gRPC (with a generated Go client) is the primary transport; a thin HTTP/JSON
+ SSE facade is added for the web UI. All mutating operations return a **job id**
and run asynchronously.

```
service Library {
  // sources
  rpc RegisterSource(RegisterSourceReq) returns (Source);   // kind: seed|backup|inbox
  rpc ListSources(Empty) returns (SourceList);

  // heavy work -> job
  rpc Ingest(IngestReq)      returns (Job);   // scan+hash+dedupe one source
  rpc BuildSeed(BuildReq)    returns (Job);   // cluster + aggregate the seed
  rpc Merge(MergeReq)        returns (Job);   // ingest external dir, match, plan
  rpc Commit(CommitReq)      returns (Commit);// commit current plan (atomic)
  rpc Apply(ApplyReq)        returns (Job);   // materialize; dry_run flag

  // inspection
  rpc Plan(PlanReq)          returns (Plan);  // per-picture plan + conflicts
  rpc Pictures(ListReq)      returns (stream Picture);
  rpc Conflicts(PlanReq)     returns (ConflictList);
  rpc Job(JobID)             returns (Job);   // status + progress + tail log
}
```

### 4.3 Async jobs and progress

- Each job is a goroutine (or small worker set) with a `context.Context` for
  cancellation.
- Job state: `queued → running → done | error | cancelled`, plus a progress
  fraction and a rolling log tail. Progress is pushed to the client over SSE /
  a gRPC server-streaming `Job.Watch`.
- Jobs are idempotent where possible (re-ingesting an unchanged source is a no-op
  thanks to the (path,size,mtime) hash cache).

### 4.4 Concurrency model

- **Hashing**: a bounded goroutine pool (≈ `runtime.NumCPU()`, cap 32) over the
  pending files; results collected over a channel. Matches the prototype's
  thread pool.
- **EXIF**: a **single** long-lived `exiftool -stay_open True -@ -` process,
  guarded by a mutex, fed batched commands (reads in batches of ~200 files,
  writes in batches of ~40), exactly as the prototype does. A background goroutine
  drains stderr for progress. (Section 9.3 keeps exiftool for write parity.)
- **Clustering / aggregation / merge**: single goroutine over in-memory slices;
  CPU-bound but cheap relative to decode/EXIF.

---

## 5. The library as a committed store

### 5.1 Entities

```
Source        { id, label, kind: seed|backup|inbox, path, added_at }
File          { id, source_id, path, size, mtime_ns, width, height,
                hash [32]byte, kind }
Picture       { id, stored_name,
                pixel_file_id, name_src,
                capture_time, capture_file_id,
                gps_lat, gps_lon, gps_file_id,
                camera_file_id,
                conflicts []string, max_hamming }
PictureMember { picture_id, file_id, role, hamming_to_medoid }
                // role ∈ pixel|name|capture|gps|camera|other
Commit        { id, at, summary }
CommitAction  { commit_id, picture_id, action: new|renamed|updated|untouched,
                prev_name, new_name }
```

`Picture` is the canonical, de-duplicated unit. `File` rows are the physical
members (including dropped exact copies, tagged by role/source for provenance).
Provenance (which backup/source a member came from) is kept on `File` so a
rebuild can show *where* the winning pixels/metadata came from.

### 5.2 Commit model

- The **working plan** (output of BuildSeed or Merge) lives in memory / a
  scratch table until `Commit`.
- `Commit` is **atomic** (one DB transaction): replace the picture index, append
  a `Commit` row and its `CommitAction` rows. It yields a diff (how many
  new/renamed/updated/untouched) and is the unit of audit and rollback.
- `Apply` is driven by the **committed** plan, so materialization always matches
  a committed state. `Apply` with `dry_run` reports what would change without
  writing.
- Rollback: because every commit stores the full index, an earlier commit can be
  re-applied as the source of truth (the store is content-addressed by picture
  id, not by mutation history).

### 5.3 Storage (SQLite)

Single-file SQLite via `database/sql` + a modern driver. Rationale: transactional,
zero-server, easy to back up (copy the file), and the index (not the pixels) is
what's stored — photos stay in place.

```sql
CREATE TABLE sources        (id INTEGER PK, label TEXT, kind TEXT, path TEXT, added_at INT);
CREATE TABLE files          (id INTEGER PK, source_id INT, path TEXT UNIQUE, size INT,
                             mtime_ns INT, width INT, height INT, hash BLOB, kind TEXT);
CREATE INDEX idx_files_hash ON files(hash);
CREATE TABLE pictures       (id INTEGER PK, stored_name TEXT, pixel_file_id INT,
                             name_src TEXT, capture_time TEXT, capture_file_id INT,
                             gps_lat REAL, gps_lon REAL, gps_file_id INT,
                             camera_file_id INT, conflicts TEXT, max_hamming INT);
CREATE TABLE picture_members(picture_id INT, file_id INT, role TEXT,
                             hamming_to_medoid INT, PRIMARY KEY(picture_id, file_id));
CREATE TABLE commits        (id INTEGER PK, at INT, summary TEXT);
CREATE TABLE commit_actions (commit_id INT, picture_id INT, action TEXT,
                             prev_name TEXT, new_name TEXT);
CREATE TABLE phash_cache    (path TEXT, size INT, mtime_ns INT, hash BLOB,
                             width INT, height INT, PRIMARY KEY(path, size, mtime_ns));
```

---

## 6. Stepwise workflow

### Phase 1 — Build the seed

```
RegisterSource(kind=seed, path=PHOTOS)
Ingest(seed)            -> hash + dedupe exact copies
BuildSeed()             -> cluster (Hamming CC) + PlanGroup per cluster
                          -> working plan with conflicts surfaced
review Conflicts()      -> operator inspects capture/GPS conflicts
Commit()                -> atomic snapshot = library v1
Apply(dry_run=true)     -> sanity check
Apply()                 -> materialize the seed library into RESULT/
```

### Phase 2 — Commit (already the end of Phase 1, and repeated after every merge)

Commit is the barrier between "planned" and "library". Nothing in `RESULT/`
changes until `Apply`, and `Apply` only ever realizes a committed plan.

### Phase 3 — Merge external / backup directories

```
RegisterSource(kind=backup, path=TM_BACKUP_2024)
Merge(backup)           ->
    1. hash incoming (cache-aware)
    2. dedupe incoming among themselves
    3. group incoming (Hamming CC, transitive)
    4. match each incoming picture vs library members
         present | join library group (re-aggregate) | new cluster
    5. PlanGroup over (existing members + joined members)  // better pixels/meta win
    6. plan actions: new / renamed / updated / untouched
Commit()                -> library v2 (diff vs v1 recorded)
Apply()                 -> copy new files, rename/update changed ones in RESULT/
```

"Better" is not a special case — it falls out of `PlanGroup` re-running over the
union of members: larger resolution wins pixels (2.3), the capture/GPS/camera
rules pick the right time/position/tags, and other-tags fill in missing
descriptions. The library stays de-duplicated because exact copies are dropped at
ingest and near-copies join an existing picture rather than creating a new one.

### Rebuild-from-backups scenario

Backups are point-in-time snapshots, so the same picture appears across several
with different file dates and sometimes different (damaged) metadata. Rebuild is
just repeated Phase 3 over the snapshots in chronological order:

```
for snap in sorted(backup_snapshots):
    RegisterSource(kind=backup, path=snap)
    Merge(snap)          # each snap's copies match the existing picture
    Commit()
Apply()                  # once, at the end, materialize the fully-merged library
```

Because of invariants T1–T4, a later snapshot whose `DateTimeOriginal` was reset
or whose file dates are wrong **cannot** move the picture on the timeline; and a
snapshot with a higher-resolution original or a GPS tag **upgrades** the picture.
The commit log gives a per-snapshot audit of what each backup contributed.

---

## 7. Go package layout

```
go.mod  (module plr)

cmd/
  plr-server/            // main: start store, jobs, gRPC/HTTP
  plr-cli/               // main: thin client over the API

internal/
  domain/                // S5 + S6 core: types, PlanGroup, choosePixel/Name,
                         //   capture/GPS/camera rules, incremental partition.
                         //   PURE. Heavily unit-tested vs prototype vectors.
  hash/                  // S2: DCT phash, Hamming, banding candidate index
  ingest/                // S1: walk, sniff kind, stat, dedupe
  cluster/               // S3: connected components (uses hash banding)
  meta/                  // S4: exiftool engine (stay_open), batch read/write,
                         //   capture-time/offset/GPS parsing
  store/                 // S7: SQLite schema, repo, commit tx
  apply/                 // S8: materialize, rename-vacate ordering, report
  server/                // S9: gRPC/HTTP handlers, job runner, progress
  client/                // S9: API client + CLI commands
```

`domain` imports nothing from `meta`/`store`/`server`; it takes plain structs and
a `Meta` lookup (`map[string]Meta`) so the spec core is isolated and testable.

---

## 8. Pseudo code (Go-flavored)

### 8.1 Domain types

```go
package domain

type Hash [32]byte // 256 bits

type Member struct {
    Path    string
    Size    int64
    MTimeNS int64
    Width   int
    Height  int
    Kind    string // jpeg|png|gif|webp|tiff|heic
    Hash    Hash
    Source  string // provenance label
}

type Meta map[string]any // one EXIF record per path, keyed "GROUP:Tag"

type Picture struct {
    ID         int
    StoredName string
    PixelSrc   Member
    NameSrc    string
    Capture    *WallTime // nil => none
    CaptureSrc string
    GPS        *Pos      // nil => none or conflict
    GPSSrc     string
    CameraSrc  string
    Conflicts  []string
    Members    []Member
    MaxHamming int
}

type WallTime struct {
    Wall      time.Time // local clock as stored (naive)
    Instant   *time.Time// absolute, when a zone or GPS time is known
    ZoneKnown bool
}

type Pos struct{ Lat, Lon float64 }

type Action string // "new" | "renamed" | "updated" | "untouched"
```

### 8.2 Phash, Hamming, banding

```go
// hash: DCT phash, 256 bits. See 9.1 for the compatibility note.
func Phash(img image.Image) Hash {
    g := toGray(img)                 // ITU-R 601-2 luma
    r := resizeLanczos(g, 64, 64)    // 16*4
    d := dct2(r)
    low := d[:16][:16]               // 16x16 low-frequency block
    med := median(low)
    var h Hash
    for i := 0; i < 256; i++ {
        if low[i/16][i%16] > med { h[i/8] |= 1 << (7 - i%8) }
    }
    return h
}

func Hamming(a, b Hash) int { return bits.OnesCount64(xorPopcount(a, b)) }

// banding: candidate generation so we don't compare all pairs.
// If two 256-bit hashes differ in <= k bits, splitting into (k+1) bands
// guarantees they are identical in at least one band (pigeonhole).
func Candidates(hashes []Hash, k int) map[int64][]int {
    bands := k + 1
    bandBits := 256 / bands
    cand := map[int64][]int{}
    for i, h := range hashes {
        for b := 0; b < bands; b++ {
            key := bandKey(h, b, bandBits) // value of band b as an int
            cand[key] = append(cand[key], i)
        }
    }
    return cand // pairs to test are those sharing a band
}
```

### 8.3 Ingest: scan + dedupe

```go
func Scan(dirs []string, minBytes int64, cache *HashCache) []Member {
    groups := map[devIno][]pathStat{}
    for each file under dirs (recursive, no symlink follow):
        if !isImageByMagic(file) { continue }
        if st.Size < minBytes { continue }
        groups[devIno(st)] = append(..., {path, st})
    // decode each inode once; fill cache by (path,size,mtime)
    for ino, paths := range groups:
        if cached := cache.get(anyOf(paths)); cached { use it }
        else { h := Phash(decodeOriented(paths[0])); cache.put(...) }
    return flatten(groups) // every hard link listed, decoded once
}

func Dedupe(recs []Member) (keep, dropped []Member) {
    byHash := groupBy(recs, func(m Member) string {
        if m.Hash == zeroHash { return "unhashed:" + m.Path }
        return m.Hash.String()
    })
    for _, items := range byHash {
        for _, sized := range groupBy(items, func(m Member) int64 { return m.Size }) {
            sort(sized, byMTimeThenPath)
            keep = append(keep, sized[0])
            for _, d := range sized[1:] { dropped = append(dropped, d) } // kept=sized[0].Path
        }
    }
    sort(keep, byPath)
    return
}
```

### 8.4 Clustering (connected components under Hamming ≤ max_hamming)

```go
func Cluster(recs []Member, maxHamming int) []Picture {
    parent := make([]int, len(recs)); iotaInit(parent)
    cand := hash.Candidates(hashesOf(recs), maxHamming)
    for _, idxs := range cand {
        for each pair (i, j) in idxs, i < j:
            if recs[i].Hash != zero && Hamming(recs[i].Hash, recs[j].Hash) <= maxHamming {
                union(parent, i, j)
            }
    }
    var pics []Picture
    for _, comp := range components(parent) {
        pics = append(pics, makePicture(recs[comp])) // medoid + members + maxHamming
    }
    return pics // a lone / unhashed file is its own 1-member picture
}

func makePicture(ms []Member) Picture {
    med, toMed, farthest := medoidDistances(ms) // min sum of Hamming
    sort(ms, byHammingToMedoidThenPath)
    return Picture{Members: ms, MaxHamming: farthest, /* medoid = ms[med] */}
}
```

### 8.5 Conflict merge — `PlanGroup` (the spec core, Section 2.3)

```go
func PlanGroup(members []Member, meta map[string]Meta, maxDisagree float64) Picture {
    p := Picture{
        Members:    members,
        PixelSrc:   choosePixel(members),
        NameSrc:    chooseName(members),
        StoredName: storedBasename(members, chooseName(members)),
    }

    // ---- capture time: only DateTimeOriginal counts ----
    type dated struct { wt WallTime; path string; hasCam bool }
    var datedList []dated
    hasCamera := false
    for _, m := range members {
        mt := meta[m.Path]
        if cameraScore(mt) > 0 { hasCamera = true }
        if wt, ok := exifCapture(mt); ok { datedList = append(datedList, dated{wt, m.Path, cameraScore(mt) > 0}) }
    }
    cameraDated := filter(datedList, func(d dated) bool { return d.hasCam })
    switch {
    case hasCamera && len(cameraDated) > 0:
        c := earliest(cameraDated) // by Instant, or Wall if mixed naive/aware
        p.Capture, p.CaptureSrc, p.CameraSrc = &c.wt, c.path, c.path
        if spanSeconds(cameraDated) > maxDisagree {
            p.Conflicts = append(p.Conflicts, "capture times disagree")
        }
    case !hasCamera && len(datedList) > 0:
        c := earliest(datedList)
        p.Capture, p.CaptureSrc = &c.wt, c.path // no conflict regardless of gap
    default:
        p.Capture = nil // camera present but no DateTimeOriginal -> none
    }

    // ---- gps ----
    var pts []struct { path string; pos Pos }
    for _, m := range members {
        if pos, ok := gpsOf(meta[m.Path]); ok { pts = append(pts, struct{...}{m.Path, pos}) }
    }
    if len(pts) > 0 {
        if anyPairFartherThan(pts, 100.0) { // haversine meters
            p.Conflicts = append(p.Conflicts, "gps positions disagree by more than 100 m")
        } else {
            best := pts[0]
            for _, q := range pts[1:] {
                if q.pos.Lat < best.pos.Lat || (q.pos.Lat == best.pos.Lat && q.pos.Lon < best.pos.Lon) {
                    best = q
                }
            }
            p.GPS, p.GPSSrc = &best.pos, best.path
        }
    }
    return p
}

func choosePixel(ms []Member) Member {
    return maxBy(ms, func(m Member) [3]int {
        return [3]int{m.Width * m.Height, encodingRank(m.Kind), int(m.Size)}
    }) // rank: heic5 jpeg4 tiff3 png2 webp1 gif0
}

func chooseName(ms []Member) string {
    return minBy(ms, func(m Member) (int, string) {
        b := base(m.Path); return len(b), strings.ToLower(b)
    })
}

// exifCapture reads DateTimeOriginal + OffsetTimeOriginal (or an offset on the
// timestamp, or GPS time as UTC). Returns the wall clock and, when a zone or
// GPS time exists, the absolute instant. CreateDate/file times are ignored.
func exifCapture(mt Meta) (WallTime, bool) { /* per 2.3 */ }

// spanSeconds: pairwise max disagreement; when one time is zoned and the other
// bare, compare wall clocks (per 2.3).
func spanSeconds(ds []dated) float64 { /* per 2.3 */ }
```

### 8.6 Incremental partition (Section 2.5)

```go
type libMember struct { group int; path string; size int64; hash Hash }

func Partition(components [][]Member, lib []libMember, maxHamming int) (
    present, matched []Member, unmatched [][]Member) {

    for _, comp := range components {
        hashed := withHash(comp)
        if len(hashed) == 0 || len(lib) == 0 { unmatched = append(unmatched, comp); continue }

        var remaining []measured
        for _, r := range hashed {
            dist := hammingToAll(r.Hash, lib) // []int, one per lib member
            sameSize := libWhere(dist == 0 && lib.size == r.Size)
            if len(sameSize) == 0 { remaining = append(remaining, measured{r, dist}); continue }
            ref := minByGroupPath(sameSize)
            present = append(present, r.withTag("present", ref.group, ref.path))
        }
        if len(remaining) == 0 { continue }

        best := minClosest(remaining)
        if best > maxHamming { unmatched = append(unmatched, stripTags(remaining)); continue }

        tied := libWhereClosest(remaining, best) // all lib members at distance == best
        chosen := minByGroupPath(tied)
        ambiguous := distinctGroups(tied) > 1
        for _, m := range remaining {
            matched = append(matched, m.withTag("matched", chosen.group,
                nearestInGroup(m, chosen.group), minDistToGroup(m, chosen.group), ambiguous))
        }
    }
    return
}
```

### 8.7 Commit

```go
func (s *Store) Commit(ctx context.Context, plan []Picture) (Commit, error) {
    prev := s.indexByGroup()                 // current committed index
    resolveNameCollisions(plan, prev)        // keep library names; prefix newcomers
    actions := markActions(plan, prev)       // new/renamed/updated/untouched (2.5.5)

    tx, _ := s.db.BeginTx(ctx, nil)
    defer tx.Rollback()
    replacePicturesTx(tx, plan)              // atomic index swap
    c := Commit{ID: newID(), At: time.Now(), Summary: summarize(actions)}
    insertCommitTx(tx, c, actions)
    if err := tx.Commit(); err != nil { return Commit{}, err }
    return c, nil
}

func markActions(plan []Picture, prev map[int]Picture) []CommitAction {
    for _, p := range plan {
        old, existed := prev[p.ID]
        switch {
        case !existed:                         p.Action = "new"
        case !sameName(old.StoredName, p.StoredName): p.Action = "renamed"
        case pixelsChanged(p, old) || metaChanged(p, old): p.Action = "updated"
        default:                               p.Action = "untouched"
        }
    }
}
```

### 8.8 Apply + rename-vacate ordering

```go
func Apply(ctx context.Context, s *Store, resultDir string, move, dryRun bool) error {
    plan := s.committedPlan()
    prefixAgainstExistingFiles(plan, resultDir) // 2.5.6

    for _, p := range placeInOrder(plan, resultDir) { // resolve rename vacates first
        dest := filepath.Join(resultDir, p.StoredName)
        switch p.Action {
        case "untouched":
            continue
        case "new":
            if exists(dest) { return errRefuseOverwrite }
            transfer(p.PixelSrc, dest, move)
        case "renamed":
            renameWithVacate(p, resultDir, move) // park-and-swap if dest is held
        case "updated":
            if !sameFile(p.PixelSrc, dest) { transfer(p.PixelSrc, dest, move) }
        }
        if !dryRun { writeChosenMeta(dest, p) } // exiftool: capture, gps, camera, fill others
    }
    if !dryRun { writeReport(resultDir, plan) }
    return nil
}

// placeInOrder: place pictures whose destination is free before pictures that
// reuse a name being vacated by a rename; park a held file aside when a cycle
// forces it. Mirrors the prototype's _place_in_order/_park_file.
func placeInOrder(plan []Picture, resultDir string) []Picture { /* 2.4 + 2.5.6 */ }
```

### 8.9 Server handlers (sketch)

```go
func (sv *Server) Merge(ctx context.Context, req *MergeReq) (*Job, error) {
    j := sv.jobs.Start("merge", func(ctx context.Context) error {
        src, _ := sv.store.Source(req.SourceID)
        cache, _  := sv.store.HashCache()
        recs := ingest.Scan([]string{src.Path}, req.MinBytes, cache)
        keep, _ := ingest.Dedupe(recs)
        comps := cluster.Components(keep, req.MaxHamming)
        lib, _  := sv.store.LibraryMembers()
        present, matched, unmatched := domain.Partition(comps, lib, req.MaxHamming)
        plan := sv.aggregateTouchedAndNew(matched, unmatched, present) // PlanGroup over unions
        sv.store.SetWorkingPlan(plan)
        return nil
    })
    return j, nil
}

func (sv *Server) Commit(ctx context.Context, req *CommitReq) (*Commit, error) {
    plan, _ := sv.store.WorkingPlan()
    return sv.store.Commit(ctx, plan)
}
```

---

## 9. Key technical decisions and open questions

**9.1 Perceptual hash in Go.** The prototype uses `imagehash.phash` (grayscale →
64×64 LANCZOS resize → 2D DCT → 16×16 low-freq → median threshold). Go has no
drop-in. **Decision:** implement a standard DCT phash with those exact parameters
in `internal/hash`. It will **not** be bit-identical to `imagehash`, but that is
acceptable because the Go system re-hashes the seed on first build and only ever
compares Go hashes to Go hashes. *Open:* if we ever need to reuse an existing
Python `phash.json` cache or cross-check against the prototype, we must match the
DCT normalization and resize filter bit-for-bit — treat as a separate, optional
task.

**9.2 Clustering engine.** **Decision:** drop HDBSCAN/OPTICS; use connected
components under "Hamming ≤ `max_hamming`" with banding (8.2/8.4). This is the
relation the prototype already enforces *after* HDBSCAN, it is deterministic and
dependency-free, and it unifies seed clustering with incremental matching.
*Trade-off:* it can bridge a chain of near-copies that HDBSCAN might have left as
noise — the same transitive behavior the prototype's post-pass and its
incremental matcher already have, so it is consistent, not a regression.
*Open:* keep an optional density-based refinement later only if real data shows
over-merging.

**9.3 Metadata engine.** **Decision:** keep **ExifTool** as a long-lived
`-stay_open` subprocess (batched reads/writes, mutex-guarded), exactly as the
prototype. Pure-Go EXIF writing is limited and would risk write-parity regressions
across the many tag groups the tool copies. *Open:* a pure-Go read path for the
common case (faster startup) is a possible optimization, but writes stay on
ExifTool.

**9.4 Storage.** **Decision:** SQLite (single file, transactional, trivially
backed up). The store holds the *index*, not the pixels. *Open:* if the library
grows past ~10^6 pictures or we want replication, move to Postgres behind the
same `store` interface.

**9.5 Transport.** **Decision:** gRPC + SSE facade. *Open:* confirm gRPC vs
plain HTTP/JSON for the first cut; the API surface (4.2) is transport-agnostic.

**9.6 Concurrency safety.** The single ExifTool process is the only shared
mutable I/O resource; it is mutex-guarded. Hashing is embarrassingly parallel.
*Open:* none expected.

---

## 10. Testing strategy

- **`domain` is the parity target.** Port the prototype's `--self-test` vectors
  (capture-time rules incl. zoned/`+14:00`/`-12:00` cases, GPS 100 m boundary,
  pixel/name choice, encoding rank, incremental present/matched/new, rename
  swap) into Go table tests. These are the regression net for Section 2.
- **`hash`**: property tests (orientation invariance, Hamming symmetry, banding
  completeness — every true near-pair is a candidate).
- **`cluster` / merge**: golden tests on synthetic hash sets, including chains and
  the "matches more than one library group" ambiguity.
- **`apply`**: temp-dir tests for copy/move/rename-vacate, including a 3-cycle
  rename (the prototype's self-test covers a 2-swap; add the longer cycle here).
- **`store`**: commit atomicity and rollback against a scratch DB.
- **End-to-end**: seed → commit → apply → merge a second dir → commit → apply,
  asserting the timeline invariants T1–T4 hold across the merge.

---

## 11. Migration / parity notes

- The Python prototype remains the reference; keep its `--self-test` passing as
  the oracle while porting. Do not delete it.
- First Go build re-hashes the seed (9.1), so there is no cache migration.
- The committed store (Section 5) is new; the prototype's `work/` JSON files
  (`unique.json`, `clusters.json`, `plan.json`, `phash.json`) map 1:1 onto the
  `files` / `pictures` / `picture_members` / `phash_cache` tables, so an importer
  can seed the Go store from an existing prototype run if desired.
```
