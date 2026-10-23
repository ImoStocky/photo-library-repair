# Chapter 11: Ingest (S1)

**Read when:** changing directory walk, magic sniff, min-size, hard-link
handling, or exact-copy de-dupe.

**Spec:** [chapter 02 §2.2 Stage A](chapter-02-semantics.md).
**Status:** Partial — see [chapter 01](chapter-01-goals-and-status.md).

## Responsibility

Walk dirs, sniff kind, stat, de-duplicate exact copies. I/O + pure dedupe.

## Spec summary

- Recursive walk; no symlink follow (design).
- Image iff magic = JPEG | PNG | GIF | WebP | TIFF | HEIC.
- `--min-size` KB (default 0).
- Same inode → decode once, list every hard link.
- Exact de-dupe: **same phash + same size** → keep earliest `(mtime, path)`;
  drop rest from clustering (still recorded).
- Different size + same phash → keep.
- Unhashed → keep as singleton later.

## Target package

`internal/ingest` — `Scan`, `Dedupe`. Uses hash cache `(path,size,mtime_ns)`.

## Current package

`internal/ingest.Scan(store, Options, Hasher)`:

- Walks dirs, extension prefilter + magic sniff (`Kind`).
- Workers call `fingerprint.Compute` (sha256 + phash + EXIF + dims).
- Upserts into `store.images`; skips already-scanned sources unless
  `--force`.
- Birth time via `birthTime` (platform-dependent).

**Gaps:** no phash cache table; exact phash+size drop happens later (and
today via **sha256** in `group.Aggregate`, not phash+size).

## Pseudocode (target)

```go
func Scan(dirs []string, minBytes int64, cache *HashCache) []Member {
    // group by (dev,ino); decode each inode once; fill cache by (path,size,mtime)
    // return every hard link listed
}

func Dedupe(recs []Member) (keep, dropped []Member) {
    // group by hash, then by size; sort by mtime then path; keep[0], drop rest
}
```

## Concurrency

Bounded pool ≈ `NumCPU()`, cap 32 (design §4.4). MVP: `--workers`.
