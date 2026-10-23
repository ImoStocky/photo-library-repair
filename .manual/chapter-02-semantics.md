# Chapter 02: Retained semantics (authoritative spec)

**MUST** be reproduced by the Go implementation. Constants match the
prototype. Clustering *engine* may follow [§9.2 / chapter 05](chapter-05-decisions.md)
(connected components) — that divergence is behavior-preserving relative to
the prototype's post-pass.

When changing rules: update this chapter, the owning S-chapter, and tests
([chapter 06](chapter-06-testing-parity.md)).

---

## 2.1 Identity

- Picture identity = **16-byte perceptual hash** (DCT phash, `hash_size=16`
  → 128 bits, czkawka-style), after **EXIF orientation**.
- Distance = **Hamming** distance on those hashes.
- Default merge radius: **`max_hamming = 12`** bits; adjustable via
  `--max-hamming`.
- JPEG may be decoded as a reduced preview (edge ≈ 64 px) for hashing speed;
  a hash hit alone never settles identity — de-dupe also requires equal file
  size (§2.2). Stored width/height remain full displayed (orientation-aware)
  size.
- Hash cache key: **`(path, size, mtime_ns)`**.

Owner: [chapter 12 — Hash](chapter-12-hash.md).

## 2.2 Grouping

### Stage A — exact-copy de-duplication (during collection)

- Recursive walk; image = **magic bytes** (JPEG/PNG/GIF/WebP/TIFF/HEIC), not
  suffix.
- `--min-size` (KB); default 0.
- Resolved path once; **hard link** same inode decoded once, still listed.
- De-dup key: **same phash AND same file size** → keep **earliest**
  `(mtime, path)`; later copies **dropped** (on disk, recorded, not clustered).
- Same phash, **different size** → keep (aggregation may prefer larger pixels).
- Hash failure → single-picture group (never lost).

Owner: [chapter 11 — Ingest](chapter-11-ingest.md).

### Stage B — near-duplicate clustering

- Connected components under **Hamming ≤ `max_hamming`** (Go decision: this
  *is* the engine; prototype used HDBSCAN/OPTICS then the same post-pass).
- Cluster records medoid (min sum of Hamming to others), per-member
  `hamming_to_medoid`, cluster `max_hamming`.
- Noise / single image = normal one-member cluster.

Owner: [chapter 13 — Cluster](chapter-13-cluster.md).

## 2.3 Conflict merge (`PlanGroup`)

One canonical picture per group. Nothing moved yet — emit a plan.
Members = “the same picture”.

**Pixels — `choosePixel`.** Largest `width × height`. Tie → encoding rank
**HEIC(5) > JPEG(4) > TIFF(3) > PNG(2) > WebP(1) > GIF(0)**. Tie → larger
file size. That file is materialized.

**Name — `chooseName`.** Shortest basename. Tie → alphabetically earlier
(case-folded). Output **extension from pixel file bytes** (sniffed kind);
substitute pixel’s canonical ext if name’s ext mismatches.

**Capture time.** Only EXIF **`DateTimeOriginal`**. Not `CreateDate`, XMP,
IPTC, or file creation time. Stored value = local wall clock
`YYYY:MM:DD HH:MM:SS`.

- Apply `OffsetTimeOriginal` when comparing; also offset on the timestamp
  (`+10:00`, `+1000`, `Z`). One zoned + one bare → compare **wall clocks**.
- “Camera data” = any of: make, model, lens make/model, lens info, focal
  length, focal length 35 mm, F-number, exposure time, ISO, exposure program,
  exposure compensation, flash, metering mode, white balance, serial number.
  Any of these → **camera file**.
- `max_capture_disagreement` default **`1d` = 86400 s** (`12h`, `90s`, or bare
  seconds accepted).

| Situation | Result |
| --- | --- |
| ≥ 2 camera files with `DateTimeOriginal` | Compare capture times. Gap > limit → conflict `"capture times disagree"`. Within limit → keep **oldest**. Non-camera later times do not enter comparison. |
| No camera file | Keep **earliest** `DateTimeOriginal`. Gap irrelevant; **no** conflict. |
| Camera data present, none of those files has `DateTimeOriginal` | **No** capture time copied. Creation time is **not** a substitute. |

Camera tags come from the file that supplied the chosen camera capture time;
else from the camera file with the **most** camera fields.

**GPS.** Lat/lon; `(0,0)` ignored; no position → no vote.

| Situation | Result |
| --- | --- |
| Any two positions **> 100 m** apart | Conflict `"gps positions disagree by more than 100 m"`; **no** GPS copied. |
| All within 100 m | Copy **smallest** coordinates: smallest lat, then smallest lon. |

**Other tags.** Description, artist, copyright, keywords, rating, title,
subject, etc. fill in where the pixel file lacks them — **not** conflict
checks.

**A conflict never drops the group.** Pixel file still materializes;
`conflicts` recorded on plan and report.

Owner: [chapter 15 — Aggregate](chapter-15-aggregate.md); meta I/O in
[chapter 14](chapter-14-meta.md).

## 2.4 Materialize (apply)

- Copy chosen pixel into result under planned name, then write capture /
  GPS / camera tags (ExifTool in target design). Source stays put.
- `--move` moves the pixel source; other members / dropped copies stay.
- **New** refuses overwrite of an existing result name. **Update**
  overwrites. **Rename** moves the existing library file.
- Report: per picture output path, sources, conflicts.

Owner: [chapter 18 — Apply](chapter-18-apply.md).

## 2.5 Incremental merge

Library work state (hash cache, cluster index, plan) is **read-only** during
merge:

1. Hash incoming (cache-aware).
2. De-dupe among themselves (distance 0 + same size → earliest; drop rest).
3. Group incoming among themselves (Hamming CC, transitive) **before**
   library compare.
4. Match each incoming picture vs **all library members** (not just medoid):

   | Closest distance | Result |
   | --- | --- |
   | **0 and same file size** | **present** |
   | **0 different size, or 1…`max_hamming`** | Whole incoming picture **joins** that library group; re-aggregate. Exact copies stay `present`. |
   | **>`max_hamming`** | **new** cluster; fresh group id above highest library id |

   Two close library groups → join **smaller** distance; equal → **lower
   group id** + conflict `"matches more than one library group"`.

5. Plan actions: `new` | `renamed` | `updated` | `untouched`.
6. Name collision on new/renamed → prefix with group number
   (`000522_IMG_0007.jpg`); original keeps name.
7. Write finished plan / clusters / hash cache **back** into the library store.

Owner: [chapter 16 — Merge](chapter-16-merge.md).

## 2.6 Timeline invariants

Load-bearing for rebuild-from-backups:

- **T1.** Stored capture time is a **pure function** of members’
  `DateTimeOriginal` + camera data (rules in 2.3). Independent of mtime,
  path, arrival order.
- **T2.** Merging a duplicate **never changes** stored capture time unless a
  camera-data member disagrees beyond `max_capture_disagreement`; then
  conflict + still keep **oldest** camera time.
- **T3.** Only `DateTimeOriginal` counts. Wrong `CreateDate`/mtime or reset
  camera clock cannot move the timeline.
- **T4.** Consistent camera copies collapse to the single earliest genuine
  capture instant.
