#!/usr/bin/env python3
"""Compare plr library groups against pre-computed Czkawka results.

Usage: compare_groups.py LIB.DB RESULTS.JSON [RESULTS.JSON...]

Expected groups are the connected components over the union of all given
results_similar_images(_compact).json files — the same picture can appear
in several runs' outputs under the same file name, so components sharing
a basename are merged. plr singletons that are not part of any expected
group are ignored; expected groups of one member cannot exist (Czkawka
only reports multi-image groups).

Exit code 0 when the partitions match exactly, 1 otherwise. MISSING lines
are expected groups plr did not reproduce; EXTRA lines are plr groups that
do not equal any expected group.
"""
import json
import os
import sqlite3
import sys


def expected(result_files):
    parent = {}

    def find(x):
        parent.setdefault(x, x)
        while parent[x] != x:
            parent[x] = parent[parent[x]]
            x = parent[x]
        return x

    def union(a, b):
        ra, rb = find(a), find(b)
        if ra != rb:
            parent[rb] = ra

    names = set()
    for rf in result_files:
        for g in json.load(open(rf)):
            ms = [os.path.basename(m["path"]) for m in g]
            names.update(ms)
            for m in ms[1:]:
                union(ms[0], m)
    comps = {}
    for n in names:
        comps.setdefault(find(n), set()).add(n)
    return [frozenset(c) for c in comps.values()]


def actual(db):
    con = sqlite3.connect(db)
    by_group = {}
    for name, gid in con.execute("select name, group_id from images"):
        by_group.setdefault(gid, set()).add(name)
    return [frozenset(g) for g in by_group.values() if len(g) > 1]


def main(db, *result_files):
    exp, act = expected(result_files), actual(db)
    es, as_ = set(exp), set(act)
    print(f"expected groups: {len(exp)}; plr multi-groups: {len(act)}; "
          f"exact matches: {len(es & as_)}")
    for g in sorted(es - as_, key=sorted):
        near = [a for a in act if a & g]
        detail = "; ".join(" ".join(sorted(a & g)) for a in near) or "all singletons"
        print("MISSING:", sorted(g), "-> plr:", detail)
    for a in sorted(as_ - es, key=sorted):
        near = [e for e in exp if a & e]
        detail = "; ".join(" ".join(sorted(e & a)) for e in near) or "none"
        print("EXTRA:", sorted(a), "-> expected:", detail)
    return 0 if es == as_ else 1


if __name__ == "__main__":
    sys.exit(main(*sys.argv[1:]))
