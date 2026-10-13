#!/usr/bin/env python3
"""Copy the sharpest file in each Czkawka similar-image group and write merged EXIF onto the copy.

Originals are only read. New files land in --output.

Requires ExifTool (Fedora: sudo dnf install perl-Image-ExifTool).

    czkawka_cli image -d /photos -g Mean -c 16 -z Lanczos3 -s 10 -p groups.json
    python merge_group_metadata.py groups.json --output /path/to/out
"""

from __future__ import annotations

import argparse
import json
import math
import re
import shutil
import subprocess
import sys
import tempfile
import threading
from datetime import datetime
from pathlib import Path

TIME_KEYS = (
    "EXIF:DateTimeOriginal",
    "EXIF:CreateDate",
    "XMP:CreateDate",
)
TIME_COPY = [
    "-DateTimeOriginal",
    "-SubSecTimeOriginal",
    "-OffsetTimeOriginal",
    "-CreateDate",
    "-DateTimeDigitized",
    "-SubSecTimeDigitized",
    "-OffsetTimeDigitized",
]
CAMERA_KEYS = (
    "EXIF:Make",
    "EXIF:Model",
    "EXIF:LensMake",
    "EXIF:LensModel",
    "EXIF:LensInfo",
    "EXIF:FocalLength",
    "EXIF:FocalLengthIn35mmFormat",
    "EXIF:FNumber",
    "EXIF:ExposureTime",
    "EXIF:ISO",
    "EXIF:ExposureProgram",
    "EXIF:ExposureCompensation",
    "EXIF:Flash",
    "EXIF:MeteringMode",
    "EXIF:WhiteBalance",
    "EXIF:SerialNumber",
)
CAMERA_COPY = [f"-{key.split(':', 1)[1]}" for key in CAMERA_KEYS]
OTHER_COPY = [
    "-ImageDescription",
    "-Artist",
    "-Copyright",
    "-UserComment",
    "-XPComment",
    "-XPTitle",
    "-XPKeywords",
    "-Keywords",
    "-Subject",
    "-Title",
    "-Rating",
    "-Caption-Abstract",
    "-ObjectName",
    "-By-line",
    "-CopyrightNotice",
]
GPS_LAT_KEYS = ("EXIF:GPSLatitude", "Composite:GPSLatitude")
GPS_LON_KEYS = ("EXIF:GPSLongitude", "Composite:GPSLongitude")
GPS_DISAGREE_M = 100.0
_EXIF_PROGRESS = re.compile(r"\[(\d+)/(\d+)\]")


class ProgressBar:
    def __init__(self, label: str, total: int):
        self.label = label
        self.total = max(total, 0)
        self.current = 0
        self.closed = False
        self._tty = sys.stderr.isatty()
        self._last = ""

    def set(self, current: int) -> None:
        if self.closed or self.total == 0:
            return
        current = min(max(current, 0), self.total)
        if current < self.current:
            return
        self.current = current
        width = 30
        filled = int(width * self.current / self.total)
        bar = "#" * filled + "-" * (width - filled)
        line = f"{self.label} [{bar}] {self.current}/{self.total}"
        if self._tty:
            pad = " " * max(0, len(self._last) - len(line))
            sys.stderr.write("\r" + line + pad)
            sys.stderr.flush()
            self._last = line
            return
        step = max(1, self.total // 20)
        if line != self._last and (self.current == self.total or self.current % step == 0):
            print(line, file=sys.stderr)
            self._last = line

    def finish(self) -> None:
        if self.closed:
            return
        self.closed = True
        if self.total == 0:
            return
        self.closed = False
        self.set(self.total)
        self.closed = True
        if self._tty:
            sys.stderr.write("\n")
            sys.stderr.flush()


def parse_groups(data: object) -> list[list[dict]]:
    """Accept Czkawka similar-image JSON, with or without reference folders."""
    if not isinstance(data, list):
        raise SystemExit("groups file must be a JSON array from czkawka_cli -p or -C")
    groups: list[list[dict]] = []
    for item in data:
        if not isinstance(item, list) or not item:
            continue
        if len(item) == 2 and isinstance(item[0], dict) and isinstance(item[1], list):
            entries = [item[0], *item[1]]
        else:
            entries = item
        files = [entry for entry in entries if isinstance(entry, dict) and entry.get("path")]
        if files:
            groups.append(files)
    return groups


def parse_capture(value: object) -> datetime | None:
    if not isinstance(value, str):
        return None
    text = value.strip().replace("T", " ")
    if len(text) < 19:
        return None
    text = text[:19]
    if text.startswith("0000"):
        return None
    for fmt in ("%Y:%m:%d %H:%M:%S", "%Y-%m-%d %H:%M:%S"):
        try:
            found = datetime.strptime(text, fmt)
        except ValueError:
            continue
        if found.year < 1990 or found.year > datetime.now().year + 1:
            return None
        return found
    return None


def capture_of(meta: dict) -> tuple[datetime | None, str | None]:
    for key in TIME_KEYS:
        found = parse_capture(meta.get(key))
        if found is not None:
            return found, key
    date = meta.get("IPTC:DateCreated")
    time_of_day = meta.get("IPTC:TimeCreated")
    if isinstance(date, str) and isinstance(time_of_day, str):
        found = parse_capture(f"{date} {time_of_day}")
        if found is not None:
            return found, "IPTC:DateCreated"
    return None, None


def first_number(meta: dict, keys: tuple[str, ...]) -> float | None:
    for key in keys:
        value = meta.get(key)
        if isinstance(value, (int, float)) and not isinstance(value, bool):
            return float(value)
    return None


def gps_of(meta: dict) -> tuple[float, float] | None:
    lat = first_number(meta, GPS_LAT_KEYS)
    lon = first_number(meta, GPS_LON_KEYS)
    if lat is None or lon is None:
        return None
    if not (-90.0 <= lat <= 90.0 and -180.0 <= lon <= 180.0):
        return None
    if lat == 0.0 and lon == 0.0:
        return None
    return lat, lon


def haversine_m(a: tuple[float, float], b: tuple[float, float]) -> float:
    radius = 6_371_000.0
    lat1, lon1 = math.radians(a[0]), math.radians(a[1])
    lat2, lon2 = math.radians(b[0]), math.radians(b[1])
    dlat = lat2 - lat1
    dlon = lon2 - lon1
    h = math.sin(dlat / 2) ** 2 + math.cos(lat1) * math.cos(lat2) * math.sin(dlon / 2) ** 2
    return 2 * radius * math.asin(math.sqrt(h))


def camera_score(meta: dict) -> int:
    return sum(1 for key in CAMERA_KEYS if meta.get(key) not in (None, ""))


def camera_identity(meta: dict) -> str:
    make = str(meta.get("EXIF:Make") or "").strip().lower()
    model = str(meta.get("EXIF:Model") or "").strip().lower()
    return f"{make} {model}".strip()


def choose_pixel(entries: list[dict]) -> dict:
    def rank(entry: dict) -> tuple[int, int]:
        width = int(entry.get("width") or 0)
        height = int(entry.get("height") or 0)
        return width * height, int(entry.get("size") or 0)

    return max(entries, key=rank)


def choose_name(entries: list[dict]) -> str:
    """Shortest basename. Equal lengths use the alphabetically earlier name."""

    def rank(entry: dict) -> tuple[int, str]:
        name = Path(str(entry["path"])).name
        return len(name), name.casefold()

    return str(min(entries, key=rank)["path"])


_KIND_SUFFIX = {
    "jpeg": ".jpg",
    "png": ".png",
    "gif": ".gif",
    "webp": ".webp",
    "tiff": ".tif",
    "heic": ".heic",
}
_KIND_ALIASES = {
    "jpeg": {".jpg", ".jpeg", ".jpe", ".jfif"},
    "png": {".png"},
    "gif": {".gif"},
    "webp": {".webp"},
    "tiff": {".tif", ".tiff"},
    "heic": {".heic", ".heif"},
}


def sniff_kind(path: Path) -> str | None:
    """Image type from file bytes. Filename suffixes are often wrong."""
    try:
        with path.open("rb") as handle:
            header = handle.read(16)
    except OSError:
        return None
    if header.startswith(b"\xff\xd8\xff"):
        return "jpeg"
    if header.startswith(b"\x89PNG\r\n\x1a\n"):
        return "png"
    if header.startswith((b"GIF87a", b"GIF89a")):
        return "gif"
    if header.startswith(b"RIFF") and header[8:12] == b"WEBP":
        return "webp"
    if header.startswith((b"II*\x00", b"MM\x00*")):
        return "tiff"
    if len(header) >= 12 and header[4:8] == b"ftyp" and header[8:12] in {
        b"heic",
        b"heix",
        b"hevc",
        b"hevx",
        b"mif1",
        b"msf1",
        b"heim",
        b"heis",
    }:
        return "heic"
    return None


def stored_basename(pixel_path: str, name_source: str) -> str:
    """Short name, with an extension ExifTool can write for these bytes."""
    pixel = Path(pixel_path)
    chosen = Path(name_source)
    kind = sniff_kind(pixel)
    if kind is None:
        aliases = {pixel.suffix.casefold()}
        suffix = pixel.suffix
    else:
        aliases = _KIND_ALIASES[kind]
        suffix = pixel.suffix if pixel.suffix.casefold() in aliases else _KIND_SUFFIX[kind]
    if chosen.suffix.casefold() in aliases:
        return chosen.name
    return f"{chosen.stem}{suffix}"


def plan_group(entries: list[dict], metadata: dict[str, dict]) -> dict:
    pixel = choose_pixel(entries)
    pixel_path = str(pixel["path"])
    name_source = choose_name(entries)
    captures: list[tuple[datetime, str]] = []
    gps_points: list[tuple[str, tuple[float, float], int]] = []
    cameras: list[tuple[str, str, int, datetime | None]] = []

    for entry in entries:
        path = str(entry["path"])
        meta = metadata.get(path, {})
        when, _source = capture_of(meta)
        if when is not None:
            captures.append((when, path))
        point = gps_of(meta)
        if point is not None:
            gps_fields = sum(1 for key in meta if key.startswith("EXIF:GPS") and meta[key] not in (None, ""))
            gps_points.append((path, point, gps_fields))
        if camera_score(meta):
            cameras.append((path, camera_identity(meta), camera_score(meta), when))

    conflicts: list[str] = []
    time_source = min(captures, key=lambda item: item[0])[1] if captures else pixel_path
    time_value = min(captures, key=lambda item: item[0])[0].strftime("%Y:%m:%d %H:%M:%S") if captures else None

    gps_source = None
    if gps_points:
        disagree = any(haversine_m(a[1], b[1]) > GPS_DISAGREE_M for a in gps_points for b in gps_points)
        if disagree:
            conflicts.append("gps positions disagree by more than 100 m")
        else:
            gps_source = max(gps_points, key=lambda item: item[2])[0]

    identities = {identity for _path, identity, _score, _when in cameras if identity}
    if len(identities) > 1:
        conflicts.append("camera make/model differs inside the group")
    camera_source = None
    if cameras:
        earliest = min((item for item in cameras if item[3] is not None), key=lambda item: item[3], default=None)
        if earliest is not None and earliest[1]:
            camera_source = earliest[0]
        else:
            camera_source = max(cameras, key=lambda item: item[2])[0]

    ordered = [path for _when, path in sorted(captures, key=lambda item: item[0])]
    for entry in entries:
        path = str(entry["path"])
        if path not in ordered:
            ordered.append(path)

    return {
        "pixel_source": pixel_path,
        "name_source": name_source,
        "stored_name": stored_basename(pixel_path, name_source),
        "capture_time": time_value,
        "capture_source": time_source if time_value else None,
        "gps_source": gps_source,
        "camera_source": camera_source,
        "other_sources": ordered,
        "conflicts": conflicts,
        "members": [str(entry["path"]) for entry in entries],
    }


def run_exiftool(args: list[str], progress: ProgressBar | None = None, base: int = 0) -> subprocess.CompletedProcess[str]:
    process = subprocess.Popen(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    stderr_parts: list[str] = []

    def consume_stderr() -> None:
        assert process.stderr is not None
        for line in process.stderr:
            stderr_parts.append(line)
            match = _EXIF_PROGRESS.search(line)
            if match and progress is not None:
                progress.set(base + int(match.group(1)))

    reader = threading.Thread(target=consume_stderr)
    reader.start()
    stdout = process.stdout.read() if process.stdout is not None else ""
    reader.join()
    return subprocess.CompletedProcess(args, process.wait(), stdout, "".join(stderr_parts))


def read_metadata(exiftool: str, paths: list[str], progress: ProgressBar | None = None) -> dict[str, dict]:
    found: dict[str, dict] = {}
    unique = list(dict.fromkeys(paths))
    for start in range(0, len(unique), 200):
        chunk = unique[start : start + 200]
        with tempfile.NamedTemporaryFile("w", encoding="utf-8", delete=False) as handle:
            handle.write("\n".join(chunk))
            list_path = handle.name
        try:
            completed = run_exiftool(
                [
                    exiftool,
                    "-progress",
                    "-json",
                    "-n",
                    "-G",
                    "-m",
                    "-api",
                    "LargeFileSupport=1",
                    "-d",
                    "%Y:%m:%d %H:%M:%S",
                    "-@",
                    list_path,
                ],
                progress,
                start,
            )
        finally:
            Path(list_path).unlink(missing_ok=True)
        if completed.returncode != 0 and not completed.stdout.strip():
            raise SystemExit(completed.stderr.strip() or "exiftool failed while reading metadata")
        payload = json.loads(completed.stdout or "[]")
        if isinstance(payload, dict):
            payload = [payload]
        for record in payload:
            source = record.get("SourceFile")
            if source:
                found[str(source)] = record
    if progress is not None:
        progress.finish()
    return found


def lookup(metadata: dict[str, dict], path: str) -> dict:
    if path in metadata:
        return metadata[path]
    resolved = str(Path(path).resolve())
    for key, record in metadata.items():
        if str(Path(key).resolve()) == resolved:
            return record
    return {}


def index_metadata(metadata: dict[str, dict], paths: list[str]) -> dict[str, dict]:
    return {path: lookup(metadata, path) for path in paths}


def assign_output_names(plans: list[dict]) -> None:
    """Keep the short name. Prefix the group number only when two groups want the same name."""
    by_name: dict[str, list[dict]] = {}
    for plan in plans:
        by_name.setdefault(plan["stored_name"].casefold(), []).append(plan)
    for group in by_name.values():
        if len(group) < 2:
            continue
        for plan in group:
            plan["stored_name"] = f"{plan['group']:06d}_{plan['stored_name']}"


def write_copies(exiftool: str, jobs: list[tuple[Path, dict]], progress: ProgressBar | None = None) -> None:
    """jobs are (destination, plan). Pixel bytes are already copied."""
    overwrite_cmds: list[list[str]] = []
    fill_cmds: list[list[str]] = []
    for dest, plan in jobs:
        dest_text = str(dest)
        cmd: list[str] = ["-overwrite_original", "-P", "-m"]
        if plan["capture_source"] and plan["capture_source"] != plan["pixel_source"]:
            cmd += ["-TagsFromFile", plan["capture_source"], *TIME_COPY]
        if plan["gps_source"] and plan["gps_source"] != plan["pixel_source"]:
            cmd += ["-TagsFromFile", plan["gps_source"], "-GPS:all"]
        if plan["camera_source"] and plan["camera_source"] != plan["pixel_source"]:
            cmd += ["-TagsFromFile", plan["camera_source"], *CAMERA_COPY]
        others = list(plan["other_sources"])
        if others and others[0] != plan["pixel_source"]:
            cmd += ["-TagsFromFile", others[0], *OTHER_COPY]
        if len(cmd) > 3:
            overwrite_cmds.append(cmd + [dest_text])
        fill_sources = [path for path in others if path != plan["pixel_source"] and path != (others[0] if others else None)]
        if fill_sources:
            fill = ["-overwrite_original", "-P", "-m", "-wm", "c"]
            for path in fill_sources:
                fill += ["-TagsFromFile", path, *OTHER_COPY]
            fill_cmds.append(fill + [dest_text])
    errors = _run_exiftool_batch(exiftool, overwrite_cmds, progress)
    if progress is not None:
        progress.finish()
    if fill_cmds:
        filling = ProgressBar("filling tags", len(fill_cmds))
        errors += _run_exiftool_batch(exiftool, fill_cmds, filling)
        filling.finish()
    if errors:
        print(f"exiftool skipped {errors} file errors; other files were still written", file=sys.stderr)


def _run_exiftool_batch(exiftool: str, commands: list[list[str]], progress: ProgressBar | None = None) -> int:
    """Write tags in batches. One bad file is reported and the rest still run."""
    errors = 0
    for start in range(0, len(commands), 40):
        chunk = commands[start : start + 40]
        args = [exiftool, "-progress"]
        for index, command in enumerate(chunk):
            args.extend(command)
            if index != len(chunk) - 1:
                args.append("-execute")
        completed = run_exiftool(args, progress, start)
        if progress is not None:
            progress.set(start + len(chunk))
        if completed.returncode == 0:
            continue
        reported = [
            line
            for line in (completed.stderr or completed.stdout).splitlines()
            if line.startswith("Error:")
        ]
        if not reported:
            reported = [(completed.stderr or completed.stdout).strip() or "exiftool failed while writing metadata"]
        errors += len(reported)
        for line in reported:
            print(line, file=sys.stderr)
    return errors


def process(groups_file: Path, output: Path, exiftool: str, limit: int | None, dry_run: bool) -> list[dict]:
    groups = parse_groups(json.loads(groups_file.read_text(encoding="utf-8")))
    if limit is not None:
        groups = groups[:limit]
    paths = [str(entry["path"]) for group in groups for entry in group]
    missing = [path for path in paths if not Path(path).is_file()]
    if missing:
        print(f"skipping {len(missing)} missing files", file=sys.stderr)
    readable = [path for path in paths if Path(path).is_file()]
    reading = ProgressBar("reading metadata", len(dict.fromkeys(readable)))
    raw = read_metadata(exiftool, readable, reading)
    metadata = index_metadata(raw, paths)

    output.mkdir(parents=True, exist_ok=True)
    planned: list[dict] = []
    for index, entries in enumerate(groups, start=1):
        present = [entry for entry in entries if Path(str(entry["path"])).is_file()]
        if not present:
            continue
        plan = plan_group(present, metadata)
        plan["group"] = index
        planned.append(plan)
    assign_output_names(planned)

    report: list[dict] = []
    jobs: list[tuple[Path, dict]] = []
    copying = None if dry_run else ProgressBar("copying", len(planned))
    for done, plan in enumerate(planned, start=1):
        dest = output / plan["stored_name"]
        record = {"output": str(dest), **plan}
        report.append(record)
        if dry_run:
            continue
        shutil.copy2(plan["pixel_source"], dest)
        jobs.append((dest, plan))
        if copying is not None:
            copying.set(done)
    if copying is not None:
        copying.finish()
    if not dry_run:
        write_copies(exiftool, jobs, ProgressBar("writing metadata", len(jobs)))
    (output / "report.json").write_text(json.dumps(report, indent=2), encoding="utf-8")
    return report


def main(argv: list[str] | None = None) -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("groups", type=Path, nargs="?", help="JSON from czkawka_cli image -p")
    parser.add_argument("--output", type=Path, help="directory for new images")
    parser.add_argument("--exiftool", default="exiftool")
    parser.add_argument("--limit", type=int, default=None)
    parser.add_argument("--dry-run", action="store_true", help="write report.json only")
    parser.add_argument("--self-test", action="store_true")
    args = parser.parse_args(argv)
    if args.self_test:
        raise SystemExit(_self_test())
    if args.groups is None or args.output is None:
        parser.error("groups JSON and --output are required")
    if shutil.which(args.exiftool) is None:
        raise SystemExit("exiftool not found. On Fedora: sudo dnf install perl-Image-ExifTool")
    report = process(args.groups, args.output, args.exiftool, args.limit, args.dry_run)
    print(f"planned {len(report)} images in {args.output}")


def _self_test() -> int:
    big = {"path": "/orig.jpg", "width": 6000, "height": 4000, "size": 100}
    small = {"path": "/export.jpg", "width": 1920, "height": 1280, "size": 10}
    meta = {
        "/orig.jpg": {"EXIF:Make": "Canon", "EXIF:Model": "EOS"},
        "/export.jpg": {
            "EXIF:DateTimeOriginal": "2019:06:14 17:32:21",
            "EXIF:GPSLatitude": -33.85,
            "EXIF:GPSLongitude": 151.21,
            "EXIF:GPSAltitude": 12,
            "EXIF:ImageDescription": "harbour",
        },
    }
    plan = plan_group([big, small], meta)
    assert plan["pixel_source"] == "/orig.jpg"
    assert plan["name_source"] == "/orig.jpg"
    assert plan["stored_name"] == "orig.jpg"
    assert plan["capture_source"] == "/export.jpg"
    assert plan["capture_time"] == "2019:06:14 17:32:21"
    assert plan["gps_source"] == "/export.jpg"
    assert plan["camera_source"] == "/orig.jpg"
    assert plan["conflicts"] == []

    meta["/orig.jpg"]["EXIF:GPSLatitude"] = -34.0
    meta["/orig.jpg"]["EXIF:GPSLongitude"] = 151.21
    plan = plan_group([big, small], meta)
    assert plan["gps_source"] is None
    assert "gps positions disagree by more than 100 m" in plan["conflicts"]

    referenced = [
        [{"path": "/a.jpg", "width": 10, "height": 10, "size": 1}, [{"path": "/b.jpg", "width": 20, "height": 20, "size": 2}]]
    ]
    groups = parse_groups(referenced)
    assert [entry["path"] for entry in groups[0]] == ["/a.jpg", "/b.jpg"]
    assert choose_pixel(groups[0])["path"] == "/b.jpg"

    plain = [[{"path": "/a.jpg"}, {"path": "/b.jpg"}]]
    assert len(parse_groups(plain)) == 1
    assert len(parse_groups([[{"path": "/solo.jpg"}]])) == 1

    long_name = {"path": "/dcim/WhatsApp Image 2019-06-14 at 17.32.21.jpg", "width": 100, "height": 100, "size": 1}
    short_name = {"path": "/dcim/IMG.jpg", "width": 10, "height": 10, "size": 1}
    named = plan_group([long_name, short_name], {})
    assert named["name_source"] == "/dcim/IMG.jpg"
    assert stored_basename("/photos/DSC.tif", "/photos/a.jpg") == "a.tif"
    with tempfile.TemporaryDirectory() as folder:
        misnamed = Path(folder) / "IMG_2777.PNG"
        misnamed.write_bytes(b"\xff\xd8\xff\xe0" + b"\x00" * 12)
        assert stored_basename(str(misnamed), str(misnamed)) == "IMG_2777.jpg"
    same = [
        {"group": 1, "stored_name": "IMG.jpg"},
        {"group": 4, "stored_name": "img.JPG"},
    ]
    assign_output_names(same)
    assert same[0]["stored_name"] == "000001_IMG.jpg"
    assert same[1]["stored_name"] == "000004_img.JPG"
    assert _EXIF_PROGRESS.search("======== /photos/a.jpg [3/10]")
    print("self-test ok")
    return 0


if __name__ == "__main__":
    main()
