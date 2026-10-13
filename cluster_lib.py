"""Load Czkawka groups into a dataframe, embed with UMAP, cluster, and export."""

from __future__ import annotations

import json
from pathlib import Path

import numpy as np
import pandas as pd
from sklearn.cluster import HDBSCAN
from sklearn.preprocessing import StandardScaler

HASH_COLUMNS = [f"hash_{index:02d}" for index in range(32)]
OPTIONAL_COLUMNS = ["size", "width", "height", "modified_date", "difference", "source_group"]
DEFAULT_FEATURES = HASH_COLUMNS + ["source_group"]


def parse_groups(data: object) -> list[list[dict]]:
    if not isinstance(data, list):
        raise ValueError("groups file must be a JSON array")
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


def hash_bytes(entry: dict) -> np.ndarray:
    raw = entry.get("hash", entry.get("hashes"))
    if isinstance(raw, list) and raw and isinstance(raw[0], list):
        raw = raw[0]
    if not isinstance(raw, list) or not raw:
        raise ValueError(f"missing hash for {entry.get('path')}")
    return np.asarray(raw, dtype=np.float64)


def hamming(left: np.ndarray, right: np.ndarray) -> int:
    xor = np.bitwise_xor(left.astype(np.uint8), right.astype(np.uint8))
    return int(np.unpackbits(xor).sum())


def load_dataframe(path: Path) -> pd.DataFrame:
    groups = parse_groups(json.loads(Path(path).read_text(encoding="utf-8")))
    rows: list[dict] = []
    hashes: list[np.ndarray] = []
    for number, group in enumerate(groups, start=1):
        for entry in group:
            digest = hash_bytes(entry)
            hashes.append(digest)
            rows.append(
                {
                    "source_group": number,
                    "path": str(entry["path"]),
                    "size": int(entry.get("size") or 0),
                    "width": int(entry.get("width") or 0),
                    "height": int(entry.get("height") or 0),
                    "modified_date": int(entry.get("modified_date") or 0),
                    "difference": int(entry.get("difference") or 0),
                    "hash": digest.astype(np.uint8).tolist(),
                }
            )
    frame = pd.DataFrame(rows)
    if frame.empty:
        raise ValueError("no images found in groups file")
    matrix = np.vstack(hashes)
    if matrix.shape[1] != len(HASH_COLUMNS):
        raise ValueError(f"expected {len(HASH_COLUMNS)} hash bytes, found {matrix.shape[1]}")
    for index, name in enumerate(HASH_COLUMNS):
        frame[name] = matrix[:, index]
    return frame


def select_groups(frame: pd.DataFrame, start: int, end: int) -> pd.DataFrame:
    if start < 1 or end < start:
        raise ValueError("group range must satisfy 1 <= start <= end")
    chosen = frame[(frame["source_group"] >= start) & (frame["source_group"] <= end)].copy()
    if chosen.empty:
        raise ValueError(f"no images in source groups {start}..{end}")
    return chosen.reset_index(drop=True)


def embed_and_cluster(
    frame: pd.DataFrame,
    columns: list[str],
    n_neighbors: int = 15,
    min_dist: float = 0.0,
    metric: str = "euclidean",
    n_components: int = 2,
    seed: int = None,
    min_cluster_size: int = 2,
    min_samples: int = 2,
) -> pd.DataFrame:
    import umap

    missing = [name for name in columns if name not in frame.columns]
    if missing:
        raise ValueError(f"unknown columns: {missing}")
    if not columns:
        raise ValueError("select at least one column")
    if n_neighbors < 2 or n_neighbors >= len(frame):
        raise ValueError(f"n_neighbors must be from 2 to {len(frame) - 1}")
    if n_components not in (2, 3):
        raise ValueError("n_components must be 2 or 3")
    if min_cluster_size < 2:
        raise ValueError("min_cluster_size must be at least 2")

    scaled = StandardScaler().fit_transform(frame.loc[:, columns].to_numpy(dtype=np.float64))
    embedding = umap.UMAP(
        n_components=n_components,
        n_neighbors=n_neighbors,
        min_dist=min_dist,
        metric=metric,
        random_state=seed,
    ).fit_transform(scaled)
    labels = HDBSCAN(min_cluster_size=min_cluster_size, min_samples=min_samples).fit_predict(embedding)
    cluster = _label_singletons(labels)

    result = frame.copy()
    result["cluster"] = cluster
    result["x"] = embedding[:, 0]
    result["y"] = embedding[:, 1]
    if n_components == 3:
        result["z"] = embedding[:, 2]
    return result


def _label_singletons(labels: np.ndarray) -> np.ndarray:
    """Give each unclustered image its own id so it still exports as a group."""
    cluster = labels.astype(np.int64).copy()
    next_id = int(cluster[cluster >= 0].max()) + 1 if np.any(cluster >= 0) else 0
    for index, label in enumerate(cluster):
        if label < 0:
            cluster[index] = next_id
            next_id += 1
    return cluster


def to_czkawka_groups(frame: pd.DataFrame) -> list[list[dict]]:
    if "cluster" not in frame.columns:
        raise ValueError("run clustering before export")
    groups: list[list[dict]] = []
    for _, part in frame.groupby("cluster", sort=True):
        hashes = [np.asarray(value, dtype=np.uint8) for value in part["hash"]]
        base = hashes[0]
        cluster: list[dict] = []
        for offset, row in enumerate(part.itertuples(index=False)):
            cluster.append(
                {
                    "path": row.path,
                    "size": int(row.size),
                    "width": int(row.width),
                    "height": int(row.height),
                    "modified_date": int(row.modified_date),
                    "hash": hashes[offset].tolist(),
                    "difference": 0 if offset == 0 else hamming(base, hashes[offset]),
                }
            )
        groups.append(cluster)
    return groups


def write_groups(path: Path, groups: list[list[dict]]) -> None:
    Path(path).write_text(json.dumps(groups), encoding="utf-8")


def summarize(frame: pd.DataFrame) -> dict:
    sizes = frame.groupby("cluster").size()
    return {
        "images": int(len(frame)),
        "source_groups": int(frame["source_group"].nunique()),
        "clusters": int(sizes.shape[0]),
        "multi_image_clusters": int((sizes >= 2).sum()),
        "one_image_clusters": int((sizes == 1).sum()),
        "largest_cluster": int(sizes.max()),
    }
