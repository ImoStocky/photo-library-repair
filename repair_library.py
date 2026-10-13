#!/usr/bin/env python3
"""Repair a photo library in four steps. Only the last step moves files.

    python repair_library.py collect --dir PHOTO --work work
    python repair_library.py cluster --work work
    python repair_library.py aggregate --work work
    python repair_library.py apply --work work --result repaired
    python repair_library.py all --dir PHOTO --work work --result repaired

Collection keeps the earliest byte-identical file. Clustering uses a 256-bit
perceptual hash and HDBSCAN with Hamming distance. Groups farther apart than
--max-hamming bits are split, so a cluster means the hashes are actually close.
Aggregation plans one image per cluster, including single-image clusters.
Apply moves that image into the result directory and writes merged EXIF.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import shutil
import sys
from pathlib import Path

import numpy as np
from PIL import Image

from cluster_lib import _label_singletons
from merge_group_metadata import (
    ProgressBar,
    assign_output_names,
    capture_of,
    index_metadata,
    plan_group,
    read_metadata,
    sniff_kind,
    write_copies,
)

HASH_BITS = 256
DEFAULT_MAX_HAMMING = 32
CAPTURE_TOLERANCE_SECONDS = 2
CAPTURE_CONFLICT = "capture times disagree"


def _write_json(path: Path, payload: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(payload, indent=2), encoding="utf-8")


def _read_json(path: Path) -> object:
    if not path.is_file():
        raise SystemExit(f"missing {path}")
    return json.loads(path.read_text(encoding="utf-8"))


def register_heif() -> None:
    try:
        import pillow_heif
    except ImportError:
        return
    pillow_heif.register_heif_opener()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def iter_files(root: Path):
    for dirpath, _dirnames, filenames in os.walk(root, followlinks=False):
        for name in filenames:
            yield Path(dirpath) / name


def collect(directories: list[Path], work: Path) -> dict:
    """Hash images under directories and keep the earliest byte-identical file."""
    records: list[dict] = []
    seen: set[str] = set()
    files: list[Path] = []
    for directory in directories:
        if not directory.is_dir():
            raise SystemExit(f"not a directory: {directory}")
        for path in iter_files(directory):
            resolved = str(path.resolve())
            if resolved in seen or not path.is_file():
                continue
            seen.add(resolved)
            files.append(path)

    scanning = ProgressBar("scanning", len(files))
    for done, path in enumerate(files, start=1):
        scanning.set(done)
        if sniff_kind(path) is None:
            continue
        stat = path.stat()
        records.append(
            {
                "path": str(path.resolve()),
                "size": int(stat.st_size),
                "mtime_ns": int(stat.st_mtime_ns),
                "sha256": sha256_file(path),
            }
        )
    scanning.finish()

    grouped: dict[str, list[dict]] = {}
    for record in records:
        grouped.setdefault(record["sha256"], []).append(record)

    survivors: list[dict] = []
    dropped: list[dict] = []
    for items in grouped.values():
        items.sort(key=lambda item: (item["mtime_ns"], item["path"]))
        keeper = items[0]
        survivors.append(keeper)
        for item in items[1:]:
            dropped.append({**item, "kept": keeper["path"]})
    survivors.sort(key=lambda item: item["path"])
    payload = {"survivors": survivors, "dropped": dropped}
    _write_json(work / "unique.json", payload)
    print(
        f"collected {len(records)} images, kept {len(survivors)}, dropped {len(dropped)} exact copies",
        file=sys.stderr,
    )
    return payload


def bits_to_hex(bits: np.ndarray) -> str:
    packed = np.packbits(np.asarray(bits, dtype=np.uint8).reshape(-1)[:HASH_BITS])
    return packed.tobytes().hex()


def hex_to_bits(text: str) -> np.ndarray:
    raw = np.frombuffer(bytes.fromhex(text), dtype=np.uint8)
    bits = np.unpackbits(raw)
    if bits.size < HASH_BITS:
        raise ValueError(f"perceptual hash has {bits.size} bits")
    return bits[:HASH_BITS].astype(bool)


def perceptual_record(path: Path) -> dict:
    with Image.open(path) as image:
        rgb = image.convert("RGB")
        width, height = image.size
        import imagehash

        digest = imagehash.phash(rgb, hash_size=16)
    bits = np.asarray(digest.hash, dtype=bool).reshape(-1)
    if bits.size != HASH_BITS:
        raise ValueError(f"{path} perceptual hash has {bits.size} bits")
    return {"phash": bits_to_hex(bits), "width": int(width), "height": int(height)}


def hamming_bits(left: np.ndarray, right: np.ndarray) -> int:
    return int(np.count_nonzero(left != right))


def enforce_max_hamming(labels: np.ndarray, bits: np.ndarray, max_hamming: int) -> np.ndarray:
    """Split a density cluster where members differ by more than max_hamming bits."""
    assigned = np.full(len(labels), -1, dtype=np.int64)
    next_id = 0
    for cluster_id in sorted(set(int(label) for label in labels)):
        members = np.flatnonzero(labels == cluster_id)
        parent = list(range(len(members)))

        def find(index: int) -> int:
            while parent[index] != index:
                parent[index] = parent[parent[index]]
                index = parent[index]
            return index

        def union(left: int, right: int) -> None:
            left_root = find(left)
            right_root = find(right)
            if left_root != right_root:
                parent[right_root] = left_root

        block = bits[members]
        for left in range(len(members)):
            if left + 1 >= len(members):
                continue
            distances = np.count_nonzero(block[left] != block[left + 1 :], axis=1)
            for offset, distance in enumerate(distances):
                if int(distance) <= max_hamming:
                    union(left, left + 1 + offset)
        components: dict[int, int] = {}
        for index in range(len(members)):
            root = find(index)
            if root not in components:
                components[root] = next_id
                next_id += 1
            assigned[members[index]] = components[root]
    return assigned


def cluster_bit_matrix(
    bits: np.ndarray,
    min_cluster_size: int = 2,
    min_samples: int = 2,
    max_hamming: int = DEFAULT_MAX_HAMMING,
) -> np.ndarray:
    """Cluster perceptual-hash bits. Each noise point becomes its own cluster."""
    matrix = np.asarray(bits, dtype=bool)
    if matrix.ndim != 2:
        raise ValueError("bit matrix must be two-dimensional")
    if min_cluster_size < 2:
        raise ValueError("min_cluster_size must be at least 2")
    if min_samples < 1:
        raise ValueError("min_samples must be at least 1")
    if max_hamming < 0:
        raise ValueError("max_hamming must be at least 0")
    count = matrix.shape[0]
    if count == 0:
        return np.array([], dtype=np.int64)
    if count < min_cluster_size:
        return np.arange(count, dtype=np.int64)

    from sklearn.cluster import HDBSCAN

    labels = HDBSCAN(
        min_cluster_size=min_cluster_size,
        min_samples=min(min_samples, count),
        metric="hamming",
    ).fit_predict(matrix)
    labeled = _label_singletons(labels)
    return enforce_max_hamming(labeled, matrix, max_hamming)


def _medoid_distances(block: np.ndarray) -> tuple[int, np.ndarray, int]:
    count = block.shape[0]
    if count == 1:
        return 0, np.zeros(1, dtype=np.int64), 0
    distances = np.zeros((count, count), dtype=np.int64)
    for index in range(count):
        distances[index, index + 1 :] = np.count_nonzero(block[index] != block[index + 1 :], axis=1)
        distances[index + 1 :, index] = distances[index, index + 1 :]
    totals = distances.sum(axis=1)
    medoid = int(np.argmin(totals))
    return medoid, distances[medoid], int(distances.max())


def cluster(work: Path, min_cluster_size: int, min_samples: int, max_hamming: int) -> list[dict]:
    unique = _read_json(work / "unique.json")
    if not isinstance(unique, dict) or not isinstance(unique.get("survivors"), list):
        raise SystemExit("unique.json must contain a survivors list")
    survivors = unique["survivors"]
    register_heif()

    cache_path = work / "phash.json"
    cache = {"by_sha256": {}, "errors": {}}
    if cache_path.is_file():
        loaded = json.loads(cache_path.read_text(encoding="utf-8"))
        if isinstance(loaded, dict):
            cache["by_sha256"] = dict(loaded.get("by_sha256") or {})
            cache["errors"] = dict(loaded.get("errors") or {})

    hashing = ProgressBar("perceptual hash", len(survivors))
    for done, survivor in enumerate(survivors, start=1):
        hashing.set(done)
        digest = str(survivor["sha256"])
        if digest in cache["by_sha256"]:
            continue
        try:
            cache["by_sha256"][digest] = perceptual_record(Path(survivor["path"]))
            cache["errors"].pop(digest, None)
        except (OSError, ValueError) as error:
            cache["errors"][digest] = str(error)
        if done % 25 == 0:
            _write_json(cache_path, cache)
    hashing.finish()
    _write_json(cache_path, cache)

    hashed: list[dict] = []
    failed: list[dict] = []
    bit_rows: list[np.ndarray] = []
    for survivor in survivors:
        digest = str(survivor["sha256"])
        found = cache["by_sha256"].get(digest)
        if not found:
            failed.append(survivor)
            continue
        hashed.append({**survivor, **found})
        bit_rows.append(hex_to_bits(found["phash"]))

    labels = cluster_bit_matrix(
        np.vstack(bit_rows) if bit_rows else np.zeros((0, HASH_BITS), dtype=bool),
        min_cluster_size=min_cluster_size,
        min_samples=min_samples,
        max_hamming=max_hamming,
    )
    groups: list[dict] = []
    if len(hashed):
        order = np.argsort(labels, kind="mergesort")
        labels = labels[order]
        hashed = [hashed[int(index)] for index in order]
        bit_rows = [bit_rows[int(index)] for index in order]
        start = 0
        while start < len(hashed):
            stop = start + 1
            while stop < len(hashed) and labels[stop] == labels[start]:
                stop += 1
            block = np.vstack(bit_rows[start:stop])
            medoid, to_medoid, farthest = _medoid_distances(block)
            members = []
            for offset, item in enumerate(hashed[start:stop]):
                members.append(
                    {
                        "path": item["path"],
                        "size": int(item["size"]),
                        "width": int(item["width"]),
                        "height": int(item["height"]),
                        "mtime_ns": int(item["mtime_ns"]),
                        "sha256": item["sha256"],
                        "phash": item["phash"],
                        "hamming_to_medoid": int(to_medoid[offset]),
                    }
                )
            members.sort(key=lambda item: (item["hamming_to_medoid"], item["path"]))
            groups.append({"max_hamming": farthest, "members": members, "medoid": hashed[start + medoid]["path"]})
            start = stop

    for item in failed:
        print(f"perceptual hash failed, kept as its own cluster: {item['path']}", file=sys.stderr)
        groups.append(
            {
                "max_hamming": 0,
                "medoid": item["path"],
                "members": [
                    {
                        "path": item["path"],
                        "size": int(item["size"]),
                        "width": 0,
                        "height": 0,
                        "mtime_ns": int(item["mtime_ns"]),
                        "sha256": item["sha256"],
                        "phash": None,
                        "hamming_to_medoid": 0,
                    }
                ],
            }
        )
    groups.sort(key=lambda group: group["members"][0]["path"])
    _write_json(work / "clusters.json", groups)
    multi = sum(1 for group in groups if len(group["members"]) > 1)
    print(
        f"clustered {len(survivors)} images into {len(groups)} groups, {multi} with more than one image, "
        f"{len(cache['errors'])} hash failures, max hamming {max_hamming}",
        file=sys.stderr,
    )
    return groups


def annotate_capture_conflict(plan: dict, entries: list[dict], metadata: dict[str, dict]) -> dict:
    captures = []
    for entry in entries:
        when, _source = capture_of(metadata.get(str(entry["path"]), {}))
        if when is not None:
            captures.append(when)
    if len(captures) >= 2 and (max(captures) - min(captures)).total_seconds() > CAPTURE_TOLERANCE_SECONDS:
        conflicts = list(plan.get("conflicts") or [])
        if CAPTURE_CONFLICT not in conflicts:
            conflicts.append(CAPTURE_CONFLICT)
        plan["conflicts"] = conflicts
    return plan


def aggregate(work: Path, exiftool: str) -> list[dict]:
    groups = _read_json(work / "clusters.json")
    if not isinstance(groups, list):
        raise SystemExit("clusters.json must be a list")
    if shutil.which(exiftool) is None:
        raise SystemExit("exiftool not found")
    paths = [str(member["path"]) for group in groups for member in group.get("members", [])]
    readable = [path for path in paths if Path(path).is_file()]
    missing = len(paths) - len(readable)
    if missing:
        print(f"skipping {missing} missing files", file=sys.stderr)
    reading = ProgressBar("reading metadata", len(dict.fromkeys(readable)))
    raw = read_metadata(exiftool, readable, reading)
    metadata = index_metadata(raw, paths)

    planned: list[dict] = []
    for index, group in enumerate(groups, start=1):
        present = [member for member in group.get("members", []) if Path(str(member["path"])).is_file()]
        if not present:
            continue
        plan = plan_group(present, metadata)
        plan = annotate_capture_conflict(plan, present, metadata)
        plan["group"] = index
        plan["max_hamming"] = int(group.get("max_hamming") or 0)
        planned.append(plan)
    assign_output_names(planned)
    _write_json(work / "plan.json", planned)
    conflicts = sum(1 for plan in planned if plan.get("conflicts"))
    print(f"planned {len(planned)} images, {conflicts} with conflicts", file=sys.stderr)
    return planned


def apply_plan(work: Path, result: Path, exiftool: str, dry_run: bool) -> list[dict]:
    planned = _read_json(work / "plan.json")
    if not isinstance(planned, list):
        raise SystemExit("plan.json must be a list")
    if shutil.which(exiftool) is None:
        raise SystemExit("exiftool not found")

    destinations: list[tuple[dict, Path]] = []
    seen_names: set[str] = set()
    for plan in planned:
        name = str(plan["stored_name"])
        if name in seen_names:
            raise SystemExit(f"duplicate result name: {name}")
        seen_names.add(name)
        source = Path(str(plan["pixel_source"]))
        if not source.is_file():
            raise SystemExit(f"missing pixel source: {source}")
        dest = result / name
        if dest.exists():
            raise SystemExit(f"refusing to overwrite {dest}")
        destinations.append((plan, dest))

    report: list[dict] = []
    jobs: list[tuple[Path, dict]] = []
    if dry_run:
        for plan, dest in destinations:
            print(f"would move {plan['pixel_source']} -> {dest}", file=sys.stderr)
            report.append({"output": str(dest), **plan})
        print(f"dry-run: {len(report)} images would move into {result}", file=sys.stderr)
        return report

    result.mkdir(parents=True, exist_ok=True)
    moving = ProgressBar("moving", len(destinations))
    for done, (plan, dest) in enumerate(destinations, start=1):
        shutil.move(plan["pixel_source"], dest)
        jobs.append((dest, plan))
        report.append({"output": str(dest), **plan})
        moving.set(done)
    moving.finish()
    write_copies(exiftool, jobs, ProgressBar("writing metadata", len(jobs)))
    _write_json(result / "report.json", report)
    print(f"moved {len(report)} images into {result}", file=sys.stderr)
    return report


def _require_dirs(directories: list[Path] | None) -> list[Path]:
    if not directories:
        raise SystemExit("pass at least one --dir")
    return directories


def main(argv: list[str] | None = None) -> None:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="command", required=True)

    collect_parser = sub.add_parser("collect", help="hash images and keep the earliest exact copy")
    collect_parser.add_argument("--dir", dest="dirs", action="append", type=Path, required=True)
    collect_parser.add_argument("--work", type=Path, required=True)

    cluster_parser = sub.add_parser("cluster", help="perceptual-hash survivors and cluster by Hamming distance")
    cluster_parser.add_argument("--work", type=Path, required=True)
    cluster_parser.add_argument("--min-cluster-size", type=int, default=2)
    cluster_parser.add_argument("--min-samples", type=int, default=2)
    cluster_parser.add_argument("--max-hamming", type=int, default=DEFAULT_MAX_HAMMING)

    aggregate_parser = sub.add_parser("aggregate", help="plan one repaired image per cluster")
    aggregate_parser.add_argument("--work", type=Path, required=True)
    aggregate_parser.add_argument("--exiftool", default="exiftool")

    apply_parser = sub.add_parser("apply", help="move the chosen image into the result directory")
    apply_parser.add_argument("--work", type=Path, required=True)
    apply_parser.add_argument("--result", type=Path, required=True)
    apply_parser.add_argument("--exiftool", default="exiftool")
    apply_parser.add_argument("--dry-run", action="store_true")

    all_parser = sub.add_parser("all", help="run collect, cluster, aggregate, and apply")
    all_parser.add_argument("--dir", dest="dirs", action="append", type=Path, required=True)
    all_parser.add_argument("--work", type=Path, required=True)
    all_parser.add_argument("--result", type=Path, required=True)
    all_parser.add_argument("--exiftool", default="exiftool")
    all_parser.add_argument("--min-cluster-size", type=int, default=2)
    all_parser.add_argument("--min-samples", type=int, default=2)
    all_parser.add_argument("--max-hamming", type=int, default=DEFAULT_MAX_HAMMING)
    all_parser.add_argument("--dry-run", action="store_true", help="stop after the plan and do not move files")

    argv = list(sys.argv[1:] if argv is None else argv)
    if "--self-test" in argv:
        raise SystemExit(_self_test())
    args = parser.parse_args(argv)

    if args.command == "collect":
        collect(_require_dirs(args.dirs), args.work)
    elif args.command == "cluster":
        cluster(args.work, args.min_cluster_size, args.min_samples, args.max_hamming)
    elif args.command == "aggregate":
        aggregate(args.work, args.exiftool)
    elif args.command == "apply":
        apply_plan(args.work, args.result, args.exiftool, args.dry_run)
    elif args.command == "all":
        collect(_require_dirs(args.dirs), args.work)
        cluster(args.work, args.min_cluster_size, args.min_samples, args.max_hamming)
        aggregate(args.work, args.exiftool)
        if args.dry_run:
            apply_plan(args.work, args.result, args.exiftool, True)
            return
        apply_plan(args.work, args.result, args.exiftool, False)
    else:
        parser.error(f"unknown command {args.command}")


def _self_test() -> int:
    import tempfile

    bits = np.zeros((3, 8), dtype=bool)
    bits[2, :4] = True
    split = enforce_max_hamming(np.array([7, 7, 7]), bits, 2)
    assert split[0] == split[1]
    assert split[2] != split[0]

    same = np.zeros(HASH_BITS, dtype=bool)
    other = np.ones(HASH_BITS, dtype=bool)
    matrix = np.vstack([same, same, same, other, other])
    labels = cluster_bit_matrix(matrix, min_cluster_size=2, min_samples=2, max_hamming=32)
    assert len(set(labels[:3].tolist())) == 1
    assert len(set(labels[3:].tolist())) == 1
    assert labels[0] != labels[3]

    near = same.copy()
    near[:40] = True
    forced = enforce_max_hamming(np.array([0, 0, 0]), np.vstack([same, same, near]), 32)
    assert forced[0] == forced[1]
    assert forced[2] != forced[0]

    entries = [
        {"path": "/a.jpg", "width": 10, "height": 10, "size": 1},
        {"path": "/b.jpg", "width": 20, "height": 20, "size": 2},
    ]
    metadata = {
        "/a.jpg": {"EXIF:DateTimeOriginal": "2019:06:14 17:32:21"},
        "/b.jpg": {"EXIF:DateTimeOriginal": "2019:06:14 17:32:30"},
    }
    disagreed = annotate_capture_conflict(plan_group(entries, metadata), entries, metadata)
    assert disagreed["pixel_source"] == "/b.jpg"
    assert disagreed["capture_time"] == "2019:06:14 17:32:21"
    assert CAPTURE_CONFLICT in disagreed["conflicts"]

    metadata["/b.jpg"]["EXIF:DateTimeOriginal"] = "2019:06:14 17:32:22"
    close = annotate_capture_conflict(plan_group(entries, metadata), entries, metadata)
    assert CAPTURE_CONFLICT not in close["conflicts"]

    alone = annotate_capture_conflict(
        plan_group([entries[0]], {"/a.jpg": metadata["/a.jpg"]}),
        [entries[0]],
        {"/a.jpg": metadata["/a.jpg"]},
    )
    assert alone["pixel_source"] == "/a.jpg"
    assert alone["conflicts"] == []

    with tempfile.TemporaryDirectory() as folder:
        root = Path(folder)
        source = root / "library"
        older = source / "older"
        older.mkdir(parents=True)
        newer = source / "newer.jpg"
        kept = older / "kept.jpg"
        rng = np.random.default_rng(0)
        Image.fromarray(rng.integers(0, 256, size=(32, 32, 3), dtype=np.uint8)).save(kept, format="JPEG")
        newer.write_bytes(kept.read_bytes())
        os.utime(kept, (1_000_000_000, 1_000_000_000))
        os.utime(newer, (1_700_000_000, 1_700_000_000))
        other = source / "other.jpg"
        Image.fromarray(rng.integers(0, 256, size=(32, 32, 3), dtype=np.uint8)).save(other, format="JPEG")
        work = root / "work"
        payload = collect([source], work)
        assert len(payload["survivors"]) == 2
        assert len(payload["dropped"]) == 1
        assert payload["dropped"][0]["path"].endswith("newer.jpg")
        assert payload["dropped"][0]["kept"].endswith("kept.jpg")
        assert newer.is_file() and kept.is_file()
        groups = cluster(work, 2, 2, 0)
        assert len(groups) == 2
        assert all(len(group["members"]) == 1 for group in groups)
        plans = aggregate(work, "exiftool")
        assert len(plans) == 2
        result = root / "result"
        apply_plan(work, result, "exiftool", True)
        assert kept.is_file()
        moved = apply_plan(work, result, "exiftool", False)
        assert len(moved) == 2
        assert not kept.is_file()
        assert newer.is_file()
        assert (result / "report.json").is_file()

    print("self-test ok")
    return 0


if __name__ == "__main__":
    main()
