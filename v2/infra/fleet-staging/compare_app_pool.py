#!/usr/bin/env python3
"""Compare three read-only app-pool receipts from a reviewed 2→3→2 operation."""
import argparse
import json
from pathlib import Path


def compare(before, expanded, contracted):
    stages = (before, expanded, contracted)
    if [s.get("phase") for s in stages] != ["before", "expanded", "contracted"]:
        raise ValueError("receipts must be ordered before, expanded, contracted")
    if [s.get("appNodeCount") for s in stages] != [2, 3, 2]:
        raise ValueError("node counts must be 2→3→2")
    if any(s.get("namespace") != "norn-work-staging" or s.get("job") != "hello-norn-mysql" for s in stages):
        raise ValueError("namespace or job differs")
    ids = [set(s.get("readyEligibleAppNodeIDs", [])) for s in stages]
    if [len(v) for v in ids] != [2, 3, 2] or not ids[0] < ids[1] or not ids[2] < ids[1]:
        raise ValueError("expansion/contraction did not preserve reviewed node identity")
    if len({s.get("observedAt") for s in stages}) != 3 or not (before["observedAt"] < expanded["observedAt"] < contracted["observedAt"]):
        raise ValueError("receipts are not in observation order")
    return {"beforeNodeIDs": sorted(ids[0]), "addedNodeIDs": sorted(ids[1] - ids[0]),
            "retiredNodeIDs": sorted(ids[1] - ids[2]), "afterNodeIDs": sorted(ids[2])}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for phase in ("before", "expanded", "contracted"):
        parser.add_argument("--" + phase, required=True, type=Path)
    args = parser.parse_args()
    try:
        receipts = [json.loads(getattr(args, phase).read_text()) for phase in ("before", "expanded", "contracted")]
        print(json.dumps(compare(*receipts), sort_keys=True))
    except (OSError, ValueError, KeyError) as error:
        parser.exit(1, "app-pool comparison failed: " + str(error) + "\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
