#!/usr/bin/env python3
"""Bounded external probe; keep receipt IDs to verify acknowledged writes."""
import argparse
import concurrent.futures
import json
import ssl
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


def request(base, method, record):
    start = time.monotonic()
    req = urllib.request.Request(base + "/records/" + record, method=method)
    try:
        # Never forward synthetic writes through redirects to another origin.
        class NoRedirect(urllib.request.HTTPRedirectHandler):
            def redirect_request(self, *args, **kwargs):
                return None
        opener = urllib.request.build_opener(NoRedirect(), urllib.request.HTTPSHandler(context=ssl.create_default_context()))
        with opener.open(req, timeout=5) as response:
            payload = response.read(65537)
            if len(payload) > 65536:
                raise ValueError("response exceeds probe limit")
            data = json.loads(payload)
            if not isinstance(data, dict):
                raise ValueError("response must be an object")
            return {"ok": response.status == 200 and data.get("id") == record,
                    "ms": (time.monotonic() - start) * 1000,
                    "allocation": data.get("allocation", ""), "id": record}
    except (OSError, ValueError, urllib.error.HTTPError):
        return {"ok": False, "ms": (time.monotonic() - start) * 1000, "id": record}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", required=True)
    parser.add_argument("--rps", type=int, default=5)
    parser.add_argument("--seconds", type=int, default=60)
    parser.add_argument("--workers", type=int, default=8)
    args = parser.parse_args()
    url = urllib.parse.urlsplit(args.url)
    if url.scheme != "https" or not url.hostname or url.username or url.password or url.query or url.fragment or url.path not in ("", "/"):
        parser.error("use a reviewed HTTPS origin without credentials, query or path")
    if not 1 <= args.rps <= 50 or not 1 <= args.seconds <= 600 or not 1 <= args.workers <= 16:
        parser.error("bounds: 1–50 rps, 1–600 seconds, 1–16 workers")
    base = args.url.rstrip("/")
    run = uuid.uuid4().hex
    rows, written = [], []
    started = time.monotonic()
    # A worker-sized batch bounds pending work; saturation lowers offered load.
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.workers) as pool:
        for offset in range(0, args.rps * args.seconds, args.workers):
            if time.monotonic() >= started + args.seconds:
                break
            batch = []
            for index in range(offset, min(offset + args.workers, args.rps * args.seconds)):
                time.sleep(max(0, started + index / args.rps - time.monotonic()))
                method = "PUT" if not written or index % 10 < 3 else "GET"
                record = f"{run}-{index}" if method == "PUT" else written[index % len(written)]
                batch.append((method, pool.submit(request, base, method, record)))
            for method, future in batch:
                row = future.result(); rows.append(row)
                if method == "PUT" and row["ok"]:
                    written.append(row["id"])
            if len(rows) >= 50 and sum(not row["ok"] for row in rows[-50:]) >= 10:
                break
        # Independently re-read every acknowledged write after the exercise.
        verification = []
        verify_deadline = time.monotonic() + 60
        for offset in range(0, len(written), args.workers):
            if time.monotonic() >= verify_deadline:
                verification.extend({"id": record, "ok": False} for record in written[offset:])
                break
            verification.extend(pool.map(lambda record: request(base, "GET", record), written[offset:offset + args.workers]))
    latencies = sorted(row["ms"] for row in rows)
    def percentile(p):
        if not latencies:
            return None
        return round(latencies[min(len(latencies)-1, int((len(latencies)-1)*p))], 2)
    report = {"runId": run, "requests": len(rows), "elapsedSeconds": round(time.monotonic()-started, 2),
              "errors": sum(not row["ok"] for row in rows),
              "p50Ms": percentile(.5), "p95Ms": percentile(.95), "p99Ms": percentile(.99),
              "allocations": sorted({row.get("allocation") for row in rows if row.get("allocation")}),
              "acknowledgedWrites": written,
              "unverifiedWrites": [row["id"] for row in verification if not row["ok"]]}
    print(json.dumps(report, indent=2))
    return 1 if not rows or report["errors"] or report["unverifiedWrites"] else 0


if __name__ == "__main__":
    raise SystemExit(main())
