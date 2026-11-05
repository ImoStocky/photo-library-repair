# Chapter 15: Aggregate / PlanGroup (S5)

**Read when:** changing pixel/name/capture/GPS/camera resolution or conflict
detection. This is the **spec core**.

**Spec:** [chapter 02 §2.3](chapter-02-semantics.md) + invariants §2.6.
**Status:** Semantic diverge — MVP rules ≠ §2.3. Highest-priority parity gap.

## Responsibility

`PlanGroup`: from members + meta map → one `Picture` plan (pixels, name,
capture, GPS, camera source, conflicts). **Pure** — no I/O.

## Target package

`internal/domain` — `PlanGroup`, `choosePixel`, `chooseName`, capture/GPS
helpers. Imports only hash types + plain data.

## Current package

`internal/group.ResolveGroup` (+ helpers). Combined with clustering in the
same package. Conflicts surface via optional `Resolver` (used by
`plr plan --ai`; rules remain the default).

### MVP vs §2.3 (do not treat as equivalent)

| Concern | §2.3 | `ResolveGroup` today |
| --- | --- | --- |
| Capture source | Only `DateTimeOriginal` | EXIF else file birth (`CaptureFallback`) |
| Camera files | Explicit camera-tag set + oldest within 1d | Not modeled that way |
| Capture conflict | Camera times span > `max_capture_disagreement` | Multi-EXIF offset/GPS disagree; birth precedes EXIF by >5s |
| GPS conflict | Haversine > 100 m | Degree epsilon `1e-5` |
| GPS pick | Smallest lat then lon | Agreement only; pick not “smallest” |
| Name | Shortest basename, case-fold | Camera-like `nameScore` |
| Pixel tie-break | Encoding rank then size | Resolution / size path |
| Conflict apply | Never skip materialize | Aligned: conflicts materialize as plain copy |

## Domain types (target)

```go
type Hash [16]byte // 128 bits

type Member struct {
    Path, Kind, Source string
    Size, MTimeNS      int64
    Width, Height      int
    Hash               Hash
}

type Meta map[string]any // EXIF record per path

type Picture struct {
    ID int
    StoredName string
    PixelSrc Member
    NameSrc, CaptureSrc, GPSSrc, CameraSrc string
    Capture *WallTime
    GPS *Pos
    Conflicts []string
    Members []Member
    MaxHamming int
}

type WallTime struct {
    Wall time.Time  // local naive as stored
    Instant *time.Time
    ZoneKnown bool
}

type Pos struct{ Lat, Lon float64 }
type Action string // new|renamed|updated|untouched
```

## Pseudocode (target)

```go
func PlanGroup(members []Member, meta map[string]Meta, maxDisagree float64) Picture {
    p := Picture{
        Members: members,
        PixelSrc: choosePixel(members),
        NameSrc: chooseName(members),
        StoredName: storedBasename(members, chooseName(members)),
    }
    // capture: only DateTimeOriginal; cameraDated vs all dated; see §2.3 table
    // gps: haversine 100 m; else smallest lat/lon
    return p
}

func choosePixel(ms []Member) Member {
    // max by (width*height, encodingRank(kind), size)
    // rank: heic5 jpeg4 tiff3 png2 webp1 gif0
}

func chooseName(ms []Member) string {
    // min by (len(base), strings.ToLower(base))
}
```

## CLI

`plr plan` — writes into `groups` status/reason/pixel/capture/gps/name.
The removed `--conflict-hamming` near-identical rule is superseded by the
identity decision graph (`docs/identity-decision-graph.md`).

## When fixing parity

1. Port §2.3 into a pure function (prefer new `internal/domain` or tighten
   `group`).
2. Table-test every row of the capture/GPS tables in chapter 02.
3. Keep T1–T4 true — no file-time override of capture.
