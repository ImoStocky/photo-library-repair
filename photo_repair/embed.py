#!/usr/bin/env python3
"""Cluster Czkawka groups with UMAP and write a groups JSON file.

Defaults match the notebook: hash bytes plus the source group number, UMAP
to 2D, then HDBSCAN. One-image results are kept.

    python embed_group.py _groups.json --output clusters_groups.json --plot umap_clusters.png
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path

import matplotlib.pyplot as plt

from photo_repair.embedding import DEFAULT_FEATURES, embed_and_cluster, load_dataframe, select_groups, summarize, to_czkawka_groups, write_groups


def save_plot(frame, path: Path) -> None:
    figure, axis = plt.subplots(figsize=(8, 6))
    axis.scatter(frame["x"], frame["y"], c=frame["cluster"] % 20, s=4, cmap="tab20", linewidths=0)
    axis.set_title(f"{frame['cluster'].nunique()} clusters, {len(frame)} images")
    axis.set_xlabel("umap 1")
    axis.set_ylabel("umap 2")
    figure.tight_layout()
    figure.savefig(path, dpi=120)
    plt.close(figure)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("groups", type=Path, help="Czkawka groups.json")
    parser.add_argument("--output", type=Path, default=Path("clusters_groups.json"))
    parser.add_argument("--plot", type=Path, default=None, help="optional 2D PNG of the embedding")
    parser.add_argument("--neighbors", type=int, default=15)
    parser.add_argument("--min-dist", type=float, default=0.0)
    parser.add_argument("--metric", default="euclidean")
    parser.add_argument("--components", type=int, default=2)
    parser.add_argument("--min-cluster-size", type=int, default=2)
    parser.add_argument("--min-samples", type=int, default=2)
    parser.add_argument("--group-start", type=int, default=1)
    parser.add_argument("--group-end", type=int, default=None, help="last source group, inclusive")
    parser.add_argument("--seed", type=int, default=0)
    args = parser.parse_args()

    frame = load_dataframe(args.groups)
    end = args.group_end if args.group_end is not None else int(frame["source_group"].max())
    chosen = select_groups(frame, args.group_start, end)
    print(
        f"images {len(chosen)}  source groups {chosen['source_group'].nunique()}  features {len(DEFAULT_FEATURES)}",
        file=sys.stderr,
    )
    result = embed_and_cluster(
        chosen,
        DEFAULT_FEATURES,
        n_neighbors=args.neighbors,
        min_dist=args.min_dist,
        metric=args.metric,
        n_components=args.components,
        seed=args.seed,
        min_cluster_size=args.min_cluster_size,
        min_samples=args.min_samples,
    )
    groups = to_czkawka_groups(result)
    write_groups(args.output, groups)
    counts = summarize(result)
    print(
        f"wrote {counts['clusters']} groups, {counts['images']} images, "
        f"{counts['one_image_clusters']} one-image groups, largest {counts['largest_cluster']}, to {args.output}",
        file=sys.stderr,
    )
    if args.plot is not None and args.components == 2:
        save_plot(result, args.plot)
        print(f"wrote {args.plot}", file=sys.stderr)


if __name__ == "__main__":
    main()
