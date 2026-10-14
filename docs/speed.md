# Speed

A photo is decoded once. The 256-bit perceptual hash, taken after EXIF orientation, is the identity and the clustering input. SHA-256 is not computed on the scan path, so the file is not read in full and then decoded again.

Identity is Hamming distance on that hash:

| Distance | Meaning |
| --- | --- |
| 0 | Same displayed picture. Drop it as already present. |
| 1 to `--max-hamming` | Same picture with a different encoding or size. Join the library group. |
| Greater than `--max-hamming` | Not in the library. Cluster it only with the other unmatched new photos. |

File size is stored next to the hash. Two files with distance 0 and the same size are the same result and the earliest path is kept. Distance 0 with a different size is still the same picture; the larger file can win later when the group is aggregated. A different picture does not need a byte hash to be rejected.

## One cache

`phash.json` is keyed by path, size, and modification time. The value is the hash, width, and height. A file is hashed again only when its size or modification time changes. The library cache is reused as-is. New files are appended. The cache is written when the scan finishes, not every few files.

Clustering of unmatched files reads these hashes. It does not decode them again.

## Cheaper decode

The hash shrinks the picture before measuring it, so the full-resolution pixels are unused. For JPEG, decode a reduced preview, apply the orientation to that preview, and hash the preview. HEIC and the other formats still open through the existing decoder, then the same orientation and hash. `hash_size` stays 16 so existing library hashes remain comparable.

## Library check without a second clustering

New hashes are compared with the member hashes already stored in `clusters.json`. The comparison is Hamming distance, not HDBSCAN, and not a new SHA-256 of the library. Unmatched hashes are the only ones passed to HDBSCAN.

## One ExifTool process

Aggregate reads metadata in batches of 200 files, and each batch starts a new ExifTool process. Writing tags starts another process every 40 commands. Startup, Perl compile, and loading the argument list repeat for every batch.

One process stays open for the whole run:

```text
exiftool -stay_open True -@ -
```

The same flags as today are sent once: `-json`, `-n`, `-G`, `-m`, a fixed date format, and `LargeFileSupport`. File paths are then written to that process’s stdin, followed by `-execute`. ExifTool prints one JSON block and waits. The next batch is more paths and another `-execute`. At the end, `-stay_open False` closes it.

Reading and writing share that process. A bad file is reported and the next `-execute` still runs, which is the current failure behavior. The tags read and written do not change.

## What this removes

| Current work | Replacement |
| --- | --- |
| SHA-256 of every byte in `collect` and `missing` | Not run |
| Perceptual hash of those same files afterwards | The only image pass |
| HDBSCAN over the library plus the new files | Distance check against stored library hashes |
| Rewriting `phash.json` during the scan | One write at the end |
| A new ExifTool process every 200 reads and every 40 writes | One process for the whole run |

## Order of work

1. Stop using SHA-256 as the collection identity. Record path, size, modification time, and the perceptual hash.
2. Decode JPEG through a reduced preview inside `perceptual_record`.
3. Point `missing` and increment at the library `phash.json` and `clusters.json`. Distance 0 is already present. Distance within `--max-hamming` joins that group. The rest are clustered alone.
4. Write the hash cache once per run.
5. Read and write metadata through one stay-open ExifTool process.

`docs/incremental-library.md` is the flow those steps implement.
