#!/usr/bin/env python3
"""Bounded, external and value-safe availability exercise for the MySQL pilot."""
import argparse
import concurrent.futures
import datetime
import json
import re
import shutil
import ssl
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


MAX_RESPONSE_BYTES = 65536
# Reject Nomad's convenient short-ID form: a destructive rehearsal must name
# one unambiguous allocation, not whichever prefix currently resolves.
ALLOCATION_ID = re.compile(r"^[0-9a-fA-F]{8}-(?:[0-9a-fA-F]{4}-){3}[0-9a-fA-F]{12}$")
SAFE_VERSION = re.compile(r"^[A-Za-z0-9._-]{1,128}$")
OCI_DIGEST = re.compile(r"^[a-z0-9./:_-]+@sha256:[0-9a-f]{64}$")


def opener():
    """Build a client that cannot send a synthetic write to another origin."""
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, *args, **kwargs):
            return None
    return urllib.request.build_opener(
        NoRedirect(), urllib.request.HTTPSHandler(context=ssl.create_default_context())
    )


def request(base, method, record):
    """Return receipt metadata only; never retain service error bodies."""
    start = time.monotonic()
    req = urllib.request.Request(base + "/records/" + record, method=method)
    try:
        with opener().open(req, timeout=5) as response:
            payload = response.read(MAX_RESPONSE_BYTES + 1)
            if len(payload) > MAX_RESPONSE_BYTES:
                raise ValueError("response exceeds probe limit")
            data = json.loads(payload)
            if not isinstance(data, dict):
                raise ValueError("response must be an object")
            allocation = data.get("allocation", "")
            source = data.get("version", "")
            return {
                "ok": response.status == 200 and data.get("id") == record,
                "ms": (time.monotonic() - start) * 1000,
                "allocation": allocation if isinstance(allocation, str) and ALLOCATION_ID.fullmatch(allocation) else "",
                "version": source if isinstance(source, str) and SAFE_VERSION.fullmatch(source) else "",
                "id": record,
            }
    except (OSError, ValueError, urllib.error.HTTPError):
        return {"ok": False, "ms": (time.monotonic() - start) * 1000, "id": record}


def version(base):
    """Read allocation identity without retaining an endpoint payload."""
    try:
        with opener().open(urllib.request.Request(base + "/version"), timeout=5) as response:
            payload = response.read(MAX_RESPONSE_BYTES + 1)
            if response.status != 200 or len(payload) > MAX_RESPONSE_BYTES:
                raise ValueError("invalid version response")
            data = json.loads(payload)
            if not isinstance(data, dict):
                raise ValueError("version response must be an object")
            allocation = data.get("allocation", "")
            source = data.get("version", "")
            return {
                "allocation": allocation if isinstance(allocation, str) and ALLOCATION_ID.fullmatch(allocation) else "",
                "version": source if isinstance(source, str) and SAFE_VERSION.fullmatch(source) else "",
            }
    except (OSError, ValueError, urllib.error.HTTPError):
        return {"allocation": "", "version": ""}


def stop_pilot_allocation(allocation):
    """Stop only a caller-selected app allocation, after binding it to this job."""
    if not ALLOCATION_ID.fullmatch(allocation) or not shutil.which("nomad"):
        return False
    try:
        inspect = subprocess.run(
            ["nomad", "alloc", "status", "-json", allocation],
            check=False, capture_output=True, text=True, timeout=15,
        )
        if inspect.returncode != 0 or len(inspect.stdout) > MAX_RESPONSE_BYTES:
            return False
        body = json.loads(inspect.stdout)
        job_id = body.get("JobID") if isinstance(body, dict) else ""
        if job_id != "hello-norn-mysql":
            return False
        stopped = subprocess.run(
            ["nomad", "alloc", "stop", "-yes", allocation],
            check=False, capture_output=True, text=True, timeout=30,
        )
        return stopped.returncode == 0
    except (OSError, ValueError, subprocess.TimeoutExpired):
        return False


def wait_for_two_allocations(base, stopped, seconds):
    """Require two live, post-stop identities; this proves scheduler recovery."""
    observed = set()
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        allocation = version(base)["allocation"]
        if allocation and allocation != stopped:
            observed.add(allocation)
            if len(observed) >= 2:
                return sorted(observed), True
        time.sleep(1)
    return sorted(observed), False


def exercise_load(base, run_id, rps, seconds, workers, written):
    """Offer a worker-bounded request stream and stop at a bounded error rate."""
    rows = []
    started = time.monotonic()
    aborted = False
    with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as pool:
        for offset in range(0, rps * seconds, workers):
            if time.monotonic() >= started + seconds:
                break
            batch = []
            for index in range(offset, min(offset + workers, rps * seconds)):
                time.sleep(max(0, started + index / rps - time.monotonic()))
                method = "PUT" if not written or index % 10 < 3 else "GET"
                record = f"{run_id}-{index}" if method == "PUT" else written[index % len(written)]
                batch.append((method, pool.submit(request, base, method, record)))
            for method, future in batch:
                row = future.result()
                rows.append(row)
                if method == "PUT" and row["ok"]:
                    written.append(row["id"])
            if len(rows) >= 50 and sum(not row["ok"] for row in rows[-50:]) >= 10:
                aborted = True
                break
    return rows, aborted


def verify_writes(base, records, workers):
    """Bound post-run readback so acknowledged writes remain durable evidence."""
    verified = []
    deadline = time.monotonic() + 60
    with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as pool:
        for offset in range(0, len(records), workers):
            if time.monotonic() >= deadline:
                verified.extend({"id": item, "ok": False} for item in records[offset:])
                break
            verified.extend(pool.map(lambda item: request(base, "GET", item), records[offset:offset + workers]))
    return verified


def proof(base, receipt):
    write = request(base, "PUT", receipt)
    read = request(base, "GET", receipt) if write["ok"] else {"ok": False}
    return {
        "id": receipt,
        "write": bool(write["ok"]),
        "read": bool(read["ok"]),
        "allocation": read.get("allocation", ""),
        "version": read.get("version", ""),
    }


def inspect_pilot_allocations(allocations):
    """Return bounded, value-safe Nomad proof for the allocations this run used."""
    if not allocations or not shutil.which("nomad"):
        return [], False
    proofs = []
    for allocation in sorted(allocations):
        if not ALLOCATION_ID.fullmatch(allocation):
            return proofs, False
        try:
            result = subprocess.run(
                ["nomad", "alloc", "status", "-json", allocation],
                check=False, capture_output=True, text=True, timeout=15,
            )
            if result.returncode != 0 or len(result.stdout) > MAX_RESPONSE_BYTES:
                return proofs, False
            body = json.loads(result.stdout)
            job = body.get("Job", {}) if isinstance(body, dict) else {}
            meta = job.get("Meta", {}) if isinstance(job, dict) else {}
            image = meta.get("pilot_image", "") if isinstance(meta, dict) else ""
            source = meta.get("pilot_source_version", "") if isinstance(meta, dict) else ""
            if body.get("JobID") != "hello-norn-mysql" or not OCI_DIGEST.fullmatch(image) or not SAFE_VERSION.fullmatch(source):
                return proofs, False
            proofs.append({
                "allocation": allocation,
                "job": "hello-norn-mysql",
                "clientStatus": body.get("ClientStatus", ""),
                "image": image,
                "sourceVersion": source,
                "createIndex": body.get("CreateIndex", 0),
                "modifyIndex": body.get("ModifyIndex", 0),
            })
        except (OSError, ValueError, subprocess.TimeoutExpired):
            return proofs, False
    return proofs, True


def percentile(rows, fraction):
    latencies = sorted(row["ms"] for row in rows)
    if not latencies:
        return None
    return round(latencies[min(len(latencies) - 1, int((len(latencies) - 1) * fraction))], 2)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", required=True)
    parser.add_argument("--rps", type=int, default=5)
    parser.add_argument("--seconds", type=int, default=60)
    parser.add_argument("--workers", type=int, default=8)
    parser.add_argument("--min-availability", type=float, default=0.80)
    parser.add_argument("--fault-allocation", help="explicit hello-norn-mysql allocation to stop")
    parser.add_argument("--recovery-seconds", type=int, default=180)
    args = parser.parse_args()
    url = urllib.parse.urlsplit(args.url)
    if url.scheme != "https" or not url.hostname or url.username or url.password or url.query or url.fragment or url.path not in ("", "/"):
        parser.error("use a reviewed HTTPS origin without credentials, query or path")
    if not 1 <= args.rps <= 50 or not 1 <= args.seconds <= 600 or not 1 <= args.workers <= 16:
        parser.error("bounds: 1–50 rps, 1–600 seconds, 1–16 workers")
    if not 0 < args.min_availability <= 1 or not 1 <= args.recovery_seconds <= 300:
        parser.error("min availability is (0,1]; recovery seconds is 1–300")
    if args.fault_allocation and not ALLOCATION_ID.fullmatch(args.fault_allocation):
        parser.error("fault allocation must be a Nomad allocation ID")

    base = args.url.rstrip("/")
    run_id = uuid.uuid4().hex
    started_at = datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")
    started = time.monotonic()
    pre = proof(base, f"{run_id}-pre")
    written = [pre["id"]] if pre["write"] and pre["read"] else []
    fault = {"requested": bool(args.fault_allocation), "target": args.fault_allocation or "", "stopAccepted": False, "recovered": True, "observedAllocations": []}
    if args.fault_allocation:
        fault["stopAccepted"] = stop_pilot_allocation(args.fault_allocation)
        if fault["stopAccepted"]:
            recovered, fault["recovered"] = wait_for_two_allocations(base, args.fault_allocation, args.recovery_seconds)
            fault["observedAllocations"] = recovered
        else:
            fault["recovered"] = False

    rows, aborted = exercise_load(base, run_id, args.rps, args.seconds, args.workers, written)
    post = proof(base, f"{run_id}-post")
    if post["write"] and post["read"]:
        written.append(post["id"])
    verification = verify_writes(base, written, args.workers)
    errors = sum(not row["ok"] for row in rows)
    availability = (len(rows) - errors) / len(rows) if rows else 0
    unverified = sum(not row["ok"] for row in verification)
    allocations = sorted({row.get("allocation") for row in rows if row.get("allocation")})
    allocations.extend(item for item in (pre.get("allocation"), post.get("allocation")) if item)
    allocations.extend(row.get("allocation") for row in verification if row.get("allocation"))
    if args.fault_allocation:
        allocations.append(args.fault_allocation)
    allocations = sorted(set(allocations))
    sources = sorted({row.get("version") for row in rows + verification if SAFE_VERSION.fullmatch(row.get("version", ""))})
    sources.extend(item.get("version") for item in (pre, post) if SAFE_VERSION.fullmatch(item.get("version", "")))
    nomad_proof, nomad_verified = inspect_pilot_allocations(allocations)
    sources = sorted(set(sources))
    source_verified = bool(sources) and nomad_verified and set(sources).issubset({item["sourceVersion"] for item in nomad_proof})
    report = {
        "runId": run_id,
        "origin": base,
        "startedAt": started_at,
        "endedAt": datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z"),
        "requestedLoad": {"rps": args.rps, "seconds": args.seconds, "workers": args.workers, "minAvailability": args.min_availability},
        "requests": len(rows),
        "elapsedSeconds": round(time.monotonic() - started, 2),
        "availability": round(availability, 4),
        "errors": errors,
        "abortedForErrorRate": aborted,
        "p50Ms": percentile(rows, .5),
        "p95Ms": percentile(rows, .95),
        "p99Ms": percentile(rows, .99),
        "allocations": allocations,
        "observedSourceVersions": sources,
        "nomadAllocations": nomad_proof,
        "nomadEvidenceVerified": nomad_verified,
        "sourceVersionMatchesNomad": source_verified,
        "preDatabaseProof": pre,
        "postDatabaseProof": post,
        "acknowledgedWriteCount": len(written),
        "unverifiedWriteCount": unverified,
        "fault": fault,
    }
    print(json.dumps(report, indent=2, sort_keys=True))
    return 0 if (rows and availability >= args.min_availability and pre["read"] and post["read"] and not unverified and fault["recovered"] and nomad_verified and source_verified) else 1


if __name__ == "__main__":
    raise SystemExit(main())
