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
from datetime import datetime
from pathlib import Path

import numpy as np
from PIL import Image, ImageOps

from photo_repair.embedding import _label_singletons
from photo_repair.metadata import (
    ProgressBar,
    assign_output_names,
    camera_score,
    index_metadata,
    parse_capture,
    parse_offset,
    plan_group,
    read_metadata,
    sniff_kind,
    write_copies,
)

HASH_BITS = 256
PHASH_CACHE_VERSION = 2
DEFAULT_MAX_HAMMING = 32
DEFAULT_OPTICS_MAX_EPS = 64
DEFAULT_OPTICS_EPS = 32
DEFAULT_MAX_CAPTURE_DISAGREEMENT = 86400.0
CAPTURE_CONFLICT = "capture times disagree"
_TIME_FIELDS = {
    "EXIF:DateTimeOriginal",
    "EXIF:CreateDate",
    "EXIF:ModifyDate",
    "EXIF:DateTimeDigitized",
    "XMP:CreateDate",
    "XMP:ModifyDate",
    "XMP:DateTimeOriginal",
    "XMP:DateTimeDigitized",
    "IPTC:DateCreated",
    "IPTC:TimeCreated",
    "IPTC:DigitalCreationDate",
    "IPTC:DigitalCreationTime",
}


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
    """Hash the picture as displayed. EXIF orientation is applied first."""
    with Image.open(path) as image:
        oriented = ImageOps.exif_transpose(image) or image
        rgb = oriented.convert("RGB")
        width, height = oriented.size
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
    algorithm: str = "hdbscan",
    hdbscan_epsilon: int = 0,
    hdbscan_method: str = "eom",
    allow_single_cluster: bool = False,
    optics_max_eps: int = DEFAULT_OPTICS_MAX_EPS,
    optics_cluster_method: str = "xi",
    optics_xi: float = 0.05,
    optics_eps: int = DEFAULT_OPTICS_EPS,
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
    if algorithm not in {"hdbscan", "optics"}:
        raise ValueError("algorithm must be hdbscan or optics")
    if not 0 <= hdbscan_epsilon <= HASH_BITS:
        raise ValueError(f"hdbscan_epsilon must be from 0 to {HASH_BITS}")
    if hdbscan_method not in {"eom", "leaf"}:
        raise ValueError("hdbscan_method must be eom or leaf")
    if not 0 < optics_max_eps <= HASH_BITS:
        raise ValueError(f"optics_max_eps must be from 1 to {HASH_BITS}")
    if optics_cluster_method not in {"xi", "dbscan"}:
        raise ValueError("optics_cluster_method must be xi or dbscan")
    if not 0 < optics_xi < 1:
        raise ValueError("optics_xi must be between 0 and 1")
    if not 0 <= optics_eps <= optics_max_eps:
        raise ValueError("optics_eps must be from 0 to optics_max_eps")
    count = matrix.shape[0]
    if count == 0:
        return np.array([], dtype=np.int64)
    if count < min_cluster_size:
        return np.arange(count, dtype=np.int64)

    if algorithm == "hdbscan":
        from sklearn.cluster import HDBSCAN

        labels = HDBSCAN(
            min_cluster_size=min_cluster_size,
            min_samples=min(min_samples, count),
            metric="hamming",
            cluster_selection_epsilon=hdbscan_epsilon / HASH_BITS,
            cluster_selection_method=hdbscan_method,
            allow_single_cluster=allow_single_cluster,
        ).fit_predict(matrix)
    else:
        from sklearn.cluster import OPTICS

        options: dict[str, object] = {
            "min_samples": min(min_samples, count),
            "min_cluster_size": min(min_cluster_size, count),
            "metric": "hamming",
            "max_eps": optics_max_eps / HASH_BITS,
            "cluster_method": optics_cluster_method,
        }
        if optics_cluster_method == "xi":
            options["xi"] = optics_xi
        else:
            options["eps"] = optics_eps / HASH_BITS
        labels = OPTICS(**options).fit_predict(matrix)
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


def cluster(
    work: Path,
    min_cluster_size: int = 2,
    min_samples: int = 2,
    max_hamming: int = DEFAULT_MAX_HAMMING,
    algorithm: str = "hdbscan",
    hdbscan_epsilon: int = 0,
    hdbscan_method: str = "eom",
    allow_single_cluster: bool = False,
    optics_max_eps: int = DEFAULT_OPTICS_MAX_EPS,
    optics_cluster_method: str = "xi",
    optics_xi: float = 0.05,
    optics_eps: int = DEFAULT_OPTICS_EPS,
) -> list[dict]:
    unique = _read_json(work / "unique.json")
    if not isinstance(unique, dict) or not isinstance(unique.get("survivors"), list):
        raise SystemExit("unique.json must contain a survivors list")
    survivors = unique["survivors"]
    register_heif()

    cache_path = work / "phash.json"
    cache = {"version": PHASH_CACHE_VERSION, "by_sha256": {}, "errors": {}}
    if cache_path.is_file():
        loaded = json.loads(cache_path.read_text(encoding="utf-8"))
        if isinstance(loaded, dict) and loaded.get("version") == PHASH_CACHE_VERSION:
            cache["by_sha256"] = dict(loaded.get("by_sha256") or {})
            cache["errors"] = dict(loaded.get("errors") or {})
        elif isinstance(loaded, dict) and loaded.get("by_sha256"):
            print("perceptual-hash cache ignored; it was built without EXIF orientation", file=sys.stderr)

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
        algorithm=algorithm,
        hdbscan_epsilon=hdbscan_epsilon,
        hdbscan_method=hdbscan_method,
        allow_single_cluster=allow_single_cluster,
        optics_max_eps=optics_max_eps,
        optics_cluster_method=optics_cluster_method,
        optics_xi=optics_xi,
        optics_eps=optics_eps,
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
    _write_json(
        work / "clustering.json",
        {
            "algorithm": algorithm,
            "min_cluster_size": min_cluster_size,
            "min_samples": min_samples,
            "max_hamming": max_hamming,
            "hdbscan_epsilon": hdbscan_epsilon,
            "hdbscan_method": hdbscan_method,
            "allow_single_cluster": allow_single_cluster,
            "optics_max_eps": optics_max_eps,
            "optics_cluster_method": optics_cluster_method,
            "optics_xi": optics_xi,
            "optics_eps": optics_eps,
        },
    )
    multi = sum(1 for group in groups if len(group["members"]) > 1)
    print(
        f"{algorithm} clustered {len(survivors)} images into {len(groups)} groups, "
        f"{multi} with more than one image, "
        f"{len(cache['errors'])} hash failures, max hamming {max_hamming}",
        file=sys.stderr,
    )
    return groups


def _stable_exif_value(value: object) -> object:
    if isinstance(value, list):
        return tuple(_stable_exif_value(item) for item in value)
    if isinstance(value, dict):
        return tuple(sorted((str(key), _stable_exif_value(item)) for key, item in value.items()))
    if isinstance(value, float):
        return round(value, 6)
    return value


def exif_fingerprint(meta: dict) -> tuple:
    """EXIF identity ignoring file paths and capture timestamps."""
    items = []
    for key, value in meta.items():
        if key == "SourceFile" or key.startswith(("File:", "System:", "Composite:")):
            continue
        if key in _TIME_FIELDS or "SubSec" in key or "OffsetTime" in key:
            continue
        if value in (None, ""):
            continue
        items.append((key, _stable_exif_value(value)))
    return tuple(sorted(items))


def _use_capture(plan: dict, when: datetime, path: str) -> dict:
    plan["capture_time"] = when.strftime("%Y:%m:%d %H:%M:%S")
    plan["capture_source"] = path
    return plan


def disagreement_seconds(left: datetime, right: datetime) -> float:
    """Absolute difference. Zones are used when both times can be placed on one timeline."""
    if (left.tzinfo is None) == (right.tzinfo is None):
        return abs((left - right).total_seconds())
    return abs((left.replace(tzinfo=None) - right.replace(tzinfo=None)).total_seconds())


def _span_seconds(times: list[datetime]) -> float:
    if len(times) < 2:
        return 0.0
    return max(
        disagreement_seconds(left, right)
        for index, left in enumerate(times)
        for right in times[index + 1 :]
    )


def _earliest(items: list[tuple]) -> tuple:
    aware = all(item[0].tzinfo is not None for item in items)
    naive = all(item[0].tzinfo is None for item in items)
    if aware or naive:
        return min(items, key=lambda item: item[0])
    return min(items, key=lambda item: item[0].replace(tzinfo=None))


def parse_duration(value: str) -> float:
    """Accept seconds or a number with s, m, h, or d. Example: 1d."""
    text = str(value).strip().lower()
    units = {"s": 1, "m": 60, "h": 3600, "d": 86400}
    unit = text[-1:] if text[-1:] in units else ""
    number = text[:-1] if unit else text
    try:
        amount = float(number)
    except ValueError as error:
        raise argparse.ArgumentTypeError("duration must look like 90, 30m, 12h, or 1d") from error
    if amount < 0:
        raise argparse.ArgumentTypeError("duration must be zero or greater")
    return amount * units.get(unit, 1)


def exif_capture(meta: dict) -> tuple[datetime | None, datetime | None]:
    """DateTimeOriginal and its offset. CreateDate and file times are ignored."""
    raw = meta.get("EXIF:DateTimeOriginal")
    local = parse_capture(raw)
    if local is None:
        return None, None
    offset = parse_offset(meta.get("EXIF:OffsetTimeOriginal"))
    if offset is None:
        offset = parse_offset(raw)
    instant = local.replace(tzinfo=offset) if offset is not None else local
    return instant, local


def annotate_capture_conflict(
    plan: dict,
    entries: list[dict],
    metadata: dict[str, dict],
    max_disagreement: float = DEFAULT_MAX_CAPTURE_DISAGREEMENT,
) -> dict:
    """Choose a capture time from DateTimeOriginal.

    Several files with camera data must agree within max_disagreement. The
    oldest of those times is kept, and a larger gap is a conflict. With no
    camera data, the earliest DateTimeOriginal is kept and the gap is not a
    conflict. CreateDate and file creation time are not capture times.
    """
    dated: list[tuple[datetime, str, bool, datetime]] = []
    has_camera = False
    for entry in entries:
        path = str(entry["path"])
        meta = metadata.get(path, {})
        if camera_score(meta) > 0:
            has_camera = True
        instant, local = exif_capture(meta)
        if instant is None or local is None:
            continue
        dated.append((instant, path, camera_score(meta) > 0, local))

    camera_dated = [item for item in dated if item[2]]
    if has_camera and camera_dated:
        chosen = _earliest(camera_dated)
        _instant, chosen_path, _has_camera, chosen_local = chosen
        _use_capture(plan, chosen_local, chosen_path)
        plan["camera_source"] = chosen_path
        if _span_seconds([item[0] for item in camera_dated]) <= max_disagreement:
            return plan
        conflicts = list(plan.get("conflicts") or [])
        if CAPTURE_CONFLICT not in conflicts:
            conflicts.append(CAPTURE_CONFLICT)
        plan["conflicts"] = conflicts
        return plan

    if not has_camera and dated:
        _instant, chosen_path, _has_camera, chosen_local = _earliest(dated)
        return _use_capture(plan, chosen_local, chosen_path)

    plan["capture_time"] = None
    plan["capture_source"] = None
    return plan


def conflict_group_ids(plans: list[dict]) -> set[int]:
    return {int(plan["group"]) for plan in plans if plan.get("conflicts")}


def replace_plans(previous: list[dict], recomputed: list[dict]) -> list[dict]:
    """Keep every prior plan, and replace groups that were just recomputed."""
    by_group = {int(plan["group"]): plan for plan in previous}
    for plan in recomputed:
        by_group[int(plan["group"])] = plan
    return [by_group[group] for group in sorted(by_group)]


def aggregate(
    work: Path,
    exiftool: str,
    max_capture_disagreement: float = DEFAULT_MAX_CAPTURE_DISAGREEMENT,
    conflicts_only: bool = False,
) -> list[dict]:
    groups = _read_json(work / "clusters.json")
    if not isinstance(groups, list):
        raise SystemExit("clusters.json must be a list")
    selected = list(enumerate(groups, start=1))
    previous: list[dict] | None = None
    if conflicts_only:
        loaded = _read_json(work / "plan.json")
        if not isinstance(loaded, list):
            raise SystemExit("plan.json must be a list")
        previous = loaded
        wanted = conflict_group_ids(previous)
        if not wanted:
            print("no conflict groups to recompute", file=sys.stderr)
            return previous
        selected = [(index, group) for index, group in selected if index in wanted]
        print(f"recomputing {len(selected)} conflict groups", file=sys.stderr)
    if shutil.which(exiftool) is None:
        raise SystemExit("exiftool not found")
    paths = [str(member["path"]) for _index, group in selected for member in group.get("members", [])]
    readable = [path for path in paths if Path(path).is_file()]
    missing = len(paths) - len(readable)
    if missing:
        print(f"skipping {missing} missing files", file=sys.stderr)
    reading = ProgressBar("reading metadata", len(dict.fromkeys(readable)))
    raw = read_metadata(exiftool, readable, reading)
    metadata = index_metadata(raw, paths)

    planned: list[dict] = []
    for index, group in selected:
        present = [member for member in group.get("members", []) if Path(str(member["path"])).is_file()]
        if not present:
            continue
        plan = plan_group(present, metadata)
        plan = annotate_capture_conflict(plan, present, metadata, max_capture_disagreement)
        plan["group"] = index
        plan["max_hamming"] = int(group.get("max_hamming") or 0)
        planned.append(plan)
    if previous is not None:
        planned = replace_plans(previous, planned)
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


def _add_clustering_arguments(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--algorithm", choices=("hdbscan", "optics"), default="hdbscan")
    parser.add_argument("--min-cluster-size", type=int, default=2)
    parser.add_argument("--min-samples", type=int, default=2)
    parser.add_argument(
        "--max-hamming",
        type=int,
        default=DEFAULT_MAX_HAMMING,
        help="split output groups connected by more than this many differing hash bits",
    )
    hdbscan = parser.add_argument_group("HDBSCAN")
    hdbscan.add_argument(
        "--hdbscan-epsilon",
        type=int,
        default=0,
        help="merge clusters within this many hash bits",
    )
    hdbscan.add_argument("--hdbscan-method", choices=("eom", "leaf"), default="eom")
    hdbscan.add_argument("--allow-single-cluster", action="store_true")
    optics = parser.add_argument_group("OPTICS")
    optics.add_argument(
        "--optics-max-eps",
        type=int,
        default=DEFAULT_OPTICS_MAX_EPS,
        help="maximum neighborhood radius in hash bits",
    )
    optics.add_argument("--optics-cluster-method", choices=("xi", "dbscan"), default="xi")
    optics.add_argument("--optics-xi", type=float, default=0.05)
    optics.add_argument(
        "--optics-eps",
        type=int,
        default=DEFAULT_OPTICS_EPS,
        help="DBSCAN extraction radius in hash bits",
    )


def _clustering_kwargs(args: argparse.Namespace) -> dict:
    return {
        "min_cluster_size": args.min_cluster_size,
        "min_samples": args.min_samples,
        "max_hamming": args.max_hamming,
        "algorithm": args.algorithm,
        "hdbscan_epsilon": args.hdbscan_epsilon,
        "hdbscan_method": args.hdbscan_method,
        "allow_single_cluster": args.allow_single_cluster,
        "optics_max_eps": args.optics_max_eps,
        "optics_cluster_method": args.optics_cluster_method,
        "optics_xi": args.optics_xi,
        "optics_eps": args.optics_eps,
    }


def main(argv: list[str] | None = None) -> None:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="command", required=True)

    collect_parser = sub.add_parser("collect", help="hash images and keep the earliest exact copy")
    collect_parser.add_argument("--dir", dest="dirs", action="append", type=Path, required=True)
    collect_parser.add_argument("--work", type=Path, required=True)

    cluster_parser = sub.add_parser("cluster", help="perceptual-hash survivors and cluster by Hamming distance")
    cluster_parser.add_argument("--work", type=Path, required=True)
    _add_clustering_arguments(cluster_parser)

    aggregate_parser = sub.add_parser("aggregate", help="plan one repaired image per cluster")
    aggregate_parser.add_argument("--work", type=Path, required=True)
    aggregate_parser.add_argument("--exiftool", default="exiftool")
    aggregate_parser.add_argument(
        "--max-capture-disagreement",
        type=parse_duration,
        default=DEFAULT_MAX_CAPTURE_DISAGREEMENT,
        help="capture times on files with camera data farther apart than this are a conflict (default 1d)",
    )
    aggregate_parser.add_argument(
        "--conflicts-only",
        action="store_true",
        help="recompute only groups already marked as conflicts in plan.json",
    )

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
    all_parser.add_argument(
        "--max-capture-disagreement",
        type=parse_duration,
        default=DEFAULT_MAX_CAPTURE_DISAGREEMENT,
        help="capture times on files with camera data farther apart than this are a conflict (default 1d)",
    )
    _add_clustering_arguments(all_parser)
    all_parser.add_argument("--dry-run", action="store_true", help="stop after the plan and do not move files")

    argv = list(sys.argv[1:] if argv is None else argv)
    if "--self-test" in argv:
        raise SystemExit(_self_test())
    args = parser.parse_args(argv)

    if args.command == "collect":
        collect(_require_dirs(args.dirs), args.work)
    elif args.command == "cluster":
        cluster(args.work, **_clustering_kwargs(args))
    elif args.command == "aggregate":
        aggregate(args.work, args.exiftool, args.max_capture_disagreement, args.conflicts_only)
    elif args.command == "apply":
        apply_plan(args.work, args.result, args.exiftool, args.dry_run)
    elif args.command == "all":
        collect(_require_dirs(args.dirs), args.work)
        cluster(args.work, **_clustering_kwargs(args))
        aggregate(args.work, args.exiftool, args.max_capture_disagreement)
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
    optics_labels = cluster_bit_matrix(
        matrix,
        min_cluster_size=2,
        min_samples=2,
        max_hamming=32,
        algorithm="optics",
        optics_max_eps=32,
        optics_cluster_method="dbscan",
        optics_eps=1,
    )
    assert len(set(optics_labels[:3].tolist())) == 1
    assert len(set(optics_labels[3:].tolist())) == 1
    assert optics_labels[0] != optics_labels[3]

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
    assert disagreed["capture_source"] == "/a.jpg"
    assert CAPTURE_CONFLICT not in disagreed["conflicts"]

    metadata["/b.jpg"]["EXIF:DateTimeOriginal"] = "2019:06:14 17:32:22"
    close = annotate_capture_conflict(plan_group(entries, metadata), entries, metadata)
    assert CAPTURE_CONFLICT not in close["conflicts"]

    camera_preferred = {
        "/a.jpg": {"EXIF:DateTimeOriginal": "2014:05:20 02:47:18"},
        "/b.jpg": {
            "EXIF:DateTimeOriginal": "2021:12:02 13:38:27",
            "EXIF:Make": "Canon",
            "EXIF:Model": "EOS",
        },
    }
    preferred = annotate_capture_conflict(plan_group(entries, camera_preferred), entries, camera_preferred)
    assert CAPTURE_CONFLICT not in preferred["conflicts"]
    assert preferred["capture_time"] == "2021:12:02 13:38:27"
    assert preferred["capture_source"] == "/b.jpg"
    assert preferred["camera_source"] == "/b.jpg"

    both_cameras = {
        "/a.jpg": {
            "EXIF:DateTimeOriginal": "2014:05:20 02:47:18",
            "EXIF:Make": "Nikon",
            "EXIF:Model": "D80",
        },
        "/b.jpg": {
            "EXIF:DateTimeOriginal": "2021:12:02 13:38:27",
            "EXIF:Make": "Canon",
            "EXIF:Model": "EOS",
        },
    }
    unresolved = annotate_capture_conflict(plan_group(entries, both_cameras), entries, both_cameras)
    assert CAPTURE_CONFLICT in unresolved["conflicts"]
    assert unresolved["capture_time"] == "2014:05:20 02:47:18"

    no_camera_far = {
        "/a.jpg": {"EXIF:DateTimeOriginal": "2014:05:18 12:00:00"},
        "/b.jpg": {"EXIF:DateTimeOriginal": "2014:05:20 12:00:00"},
    }
    far = annotate_capture_conflict(plan_group(entries, no_camera_far), entries, no_camera_far)
    assert CAPTURE_CONFLICT not in far["conflicts"]
    assert far["capture_time"] == "2014:05:18 12:00:00"
    assert far["capture_source"] == "/a.jpg"
    within = annotate_capture_conflict(plan_group(entries, no_camera_far), entries, no_camera_far, max_disagreement=3 * 86400)
    assert CAPTURE_CONFLICT not in within["conflicts"]

    same_utc = {
        "/a.jpg": {"EXIF:DateTimeOriginal": "2014:05:21 15:00:00", "EXIF:OffsetTimeOriginal": "+12:00"},
        "/b.jpg": {"EXIF:DateTimeOriginal": "2014:05:20 04:00:00", "EXIF:OffsetTimeOriginal": "+00:00"},
    }
    zoned = annotate_capture_conflict(plan_group(entries, same_utc), entries, same_utc)
    assert CAPTURE_CONFLICT not in zoned["conflicts"]
    assert zoned["capture_source"] == "/b.jpg"

    zone_gap = {
        "/a.jpg": {
            "EXIF:DateTimeOriginal": "2014:05:20 12:00:00",
            "EXIF:OffsetTimeOriginal": "+14:00",
            "EXIF:Make": "Canon",
            "EXIF:Model": "EOS",
        },
        "/b.jpg": {
            "EXIF:DateTimeOriginal": "2014:05:20 12:00:00",
            "EXIF:OffsetTimeOriginal": "-12:00",
            "EXIF:Make": "Canon",
            "EXIF:Model": "EOS",
        },
    }
    zoned_conflict = annotate_capture_conflict(plan_group(entries, zone_gap), entries, zone_gap)
    assert CAPTURE_CONFLICT in zoned_conflict["conflicts"]
    assert zoned_conflict["capture_source"] == "/a.jpg"

    duplicate_exif = {
        "/a.jpg": {
            "EXIF:DateTimeOriginal": "2014:05:20 02:47:18",
            "EXIF:Make": "Canon",
            "EXIF:Model": "EOS",
            "EXIF:FNumber": 2.8,
        },
        "/b.jpg": {
            "EXIF:DateTimeOriginal": "2021:12:02 13:38:27",
            "EXIF:Make": "Canon",
            "EXIF:Model": "EOS",
            "EXIF:FNumber": 2.8,
            "File:FileName": "copy.jpg",
        },
    }
    duplicate = annotate_capture_conflict(plan_group(entries, duplicate_exif), entries, duplicate_exif)
    assert CAPTURE_CONFLICT in duplicate["conflicts"]
    assert duplicate["capture_time"] == "2014:05:20 02:47:18"
    assert duplicate["capture_source"] == "/a.jpg"

    created_only = {
        "/a.jpg": {"EXIF:CreateDate": "2010:01:01 00:00:00"},
        "/b.jpg": {"EXIF:CreateDate": "2020:01:01 00:00:00"},
    }
    created = annotate_capture_conflict(plan_group(entries, created_only), entries, created_only)
    assert created["capture_time"] is None
    assert created["capture_source"] is None
    assert CAPTURE_CONFLICT not in created["conflicts"]

    previous = [
        {"group": 1, "conflicts": [], "stored_name": "keep.jpg"},
        {"group": 2, "conflicts": ["capture times disagree"], "stored_name": "old.jpg"},
        {"group": 4, "conflicts": ["gps positions disagree by more than 100 m"], "stored_name": "gps.jpg"},
    ]
    assert conflict_group_ids(previous) == {2, 4}
    replaced = replace_plans(previous, [{"group": 2, "conflicts": [], "stored_name": "new.jpg"}])
    assert [plan["stored_name"] for plan in replaced] == ["keep.jpg", "new.jpg", "gps.jpg"]
    assert replaced[1]["conflicts"] == []

    alone = annotate_capture_conflict(
        plan_group([entries[0]], {"/a.jpg": metadata["/a.jpg"]}),
        [entries[0]],
        {"/a.jpg": metadata["/a.jpg"]},
    )
    assert alone["pixel_source"] == "/a.jpg"
    assert alone["conflicts"] == []

    with tempfile.TemporaryDirectory() as folder:
        root = Path(folder)
        display = Image.new("RGB", (48, 24))
        for x in range(12):
            for y in range(6):
                display.putpixel((x, y), (20, 180, 40))
        upright = root / "upright.jpg"
        display.save(upright, format="JPEG", quality=95)
        sideways = root / "sideways.jpg"
        stored = display.transpose(Image.Transpose.ROTATE_90)
        exif = Image.Exif()
        exif[274] = 6
        stored.save(sideways, format="JPEG", quality=95, exif=exif.tobytes())
        upright_hash = hex_to_bits(perceptual_record(upright)["phash"])
        sideways_hash = hex_to_bits(perceptual_record(sideways)["phash"])
        assert hamming_bits(upright_hash, sideways_hash) <= 12
        assert perceptual_record(upright)["width"] == 48
        assert perceptual_record(sideways)["width"] == 48

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
