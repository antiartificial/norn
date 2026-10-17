#!/usr/bin/env python3
"""Bounded public read probe across a reviewed Fleet change; no provider effects."""
import argparse
import datetime
import json
import re
import time
import urllib.parse
import urllib.request

from exercise import ALLOCATION_ID, SAFE_VERSION, opener


RECORD = re.compile(r"^[a-f0-9]{32}-(?:pre|post|[0-9]+)$")


def sample(base, record, expected_version):
    start = time.monotonic()
    try:
        with opener().open(urllib.request.Request(base + "/records/" + record), timeout=5) as response:
            raw = response.read(65537)
            if len(raw) > 65536:
                raise ValueError("oversized response")
            data = json.loads(raw)
            allocation = data.get("allocation", "") if isinstance(data, dict) else ""
            good = (response.status == 200 and isinstance(data, dict) and data.get("id") == record and
                    data.get("version") == expected_version and isinstance(allocation, str) and
                    ALLOCATION_ID.fullmatch(allocation) is not None)
    except (OSError, ValueError, json.JSONDecodeError):
        good, allocation = False, ""
    return {"ok": bool(good), "ms": round((time.monotonic() - start) * 1000, 2),
            "allocation": allocation if good else ""}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", required=True)
    parser.add_argument("--record", required=True)
    parser.add_argument("--expected-source-version", required=True)
    parser.add_argument("--rps", type=int, default=5)
    parser.add_argument("--seconds", type=int, default=180)
    args = parser.parse_args()
    url = urllib.parse.urlsplit(args.url)
    if (url.scheme != "https" or not url.hostname or url.path not in ("", "/") or url.query or
            url.fragment or url.username or url.password or not RECORD.fullmatch(args.record) or
            not SAFE_VERSION.fullmatch(args.expected_source_version)):
        parser.error("use a reviewed HTTPS origin, synthetic record and exact source version")
    if not 1 <= args.rps <= 20 or not 1 <= args.seconds <= 900:
        parser.error("probe bounds are 1–20 rps and 1–900 seconds")
    started_at = datetime.datetime.now(datetime.timezone.utc).isoformat()
    start = time.monotonic()
    rows = []
    for index in range(args.rps * args.seconds):
        time.sleep(max(0, start + index / args.rps - time.monotonic()))
        rows.append(sample(args.url.rstrip("/"), args.record, args.expected_source_version))
    latencies = sorted(row["ms"] for row in rows)
    def percentile(fraction):
        return latencies[min(len(latencies) - 1, int((len(latencies) - 1) * fraction))]
    failures = sum(not row["ok"] for row in rows)
    print(json.dumps({
        "origin": args.url.rstrip("/"), "record": args.record,
        "expectedSourceVersion": args.expected_source_version,
        "startedAt": started_at, "endedAt": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "requestedRps": args.rps, "requestedSeconds": args.seconds,
        "requests": len(rows), "failures": failures,
        "availability": round((len(rows) - failures) / len(rows), 4),
        "p95Ms": percentile(.95), "p99Ms": percentile(.99),
        "observedAllocationIDs": sorted({row["allocation"] for row in rows if row["allocation"]}),
    }, sort_keys=True))
    return 0 if failures == 0 else 1


if __name__ == "__main__":
    raise SystemExit(main())
