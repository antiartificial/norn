#!/usr/bin/env python3
"""Compare two quiesced mobility fixture inventories before target writes."""

import json
import re
import sys
from pathlib import Path

SCHEMA = "norn.mobility-fixture/v1"
SHA256 = re.compile(r"^[0-9a-f]{64}$")
ITEM_ID = re.compile(r"^[0-9a-f]{32}$")


def inventory(state, label):
    if state.get("schema") != SCHEMA:
        raise ValueError(f"{label}: unsupported state schema")
    if state.get("writerEnabled") is not False:
        raise ValueError(f"{label}: observed writer is not fenced")
    for name in ("filesMissing", "filesMismatched", "filesOrphaned"):
        if type(state.get(name)) is not int or state[name] != 0:
            raise ValueError(f"{label}: {name} must be zero")

    items = state.get("items")
    if not isinstance(items, list):
        raise ValueError(f"{label}: items are missing")
    item_map = {}
    for item in items:
        if not isinstance(item, dict) or not ITEM_ID.fullmatch(str(item.get("id", ""))) or not SHA256.fullmatch(str(item.get("sha256", ""))):
            raise ValueError(f"{label}: invalid item identity or digest")
        if item["id"] in item_map:
            raise ValueError(f"{label}: duplicate item ID")
        item_map[item["id"]] = item["sha256"]

    def ids(field, count):
        values = state.get(field)
        if not isinstance(values, list) or any(not isinstance(value, str) for value in values) or len(values) != len(set(values)):
            raise ValueError(f"{label}: invalid {field}")
        if type(state.get(count)) is not int or state[count] != len(values):
            raise ValueError(f"{label}: {field} count differs")
        return set(values)

    pending = ids("pendingJobIds", "jobsPending")
    acknowledged = ids("acknowledgedJobIds", "jobsAcknowledged")
    ticks = ids("tickMinutes", "scheduleTicks")
    if pending & acknowledged or (pending | acknowledged) != set(item_map):
        raise ValueError(f"{label}: job identities do not partition items")
    if any(not re.fullmatch(r"\d{4}-\d\d-\d\dT\d\d:\d\d:00Z", tick) for tick in ticks):
        raise ValueError(f"{label}: invalid tick minute")
    return item_map, pending, acknowledged, ticks


def compare(source, target):
    source_items, source_pending, source_acked, source_ticks = inventory(source, "source")
    target_items, target_pending, target_acked, target_ticks = inventory(target, "target")
    if source_items != target_items:
        raise ValueError("item IDs or digests differ")
    if source_pending != target_pending or source_acked != target_acked:
        raise ValueError("pending or acknowledged job identities differ")
    if source_ticks != target_ticks:
        raise ValueError("schedule tick identities differ")
    return len(source_items), len(source_acked), len(source_ticks)


def main(argv):
    if len(argv) != 3:
        raise ValueError("usage: compare_state.py SOURCE.json TARGET.json")
    source = json.loads(Path(argv[1]).read_text())
    target = json.loads(Path(argv[2]).read_text())
    items, acked, ticks = compare(source, target)
    print(f"matching quiesced inventories: items={items} acknowledged={acked} ticks={ticks}")


if __name__ == "__main__":
    try:
        main(sys.argv)
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        print(f"mobility comparison failed: {exc}", file=sys.stderr)
        sys.exit(1)
