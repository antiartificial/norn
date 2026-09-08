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
# Fleet admits disposable run IDs as 8–24 lowercase alphanumeric characters.
# The unique Nomad namespace must be derived from that exact reviewed ID.
PILOT_NAMESPACE = re.compile(r"^norn-pilot-[a-z0-9]{8,24}$")


def opener():
    """Build a client that cannot send a synthetic write to another origin."""
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, *args, **kwargs):
            return None
    return urllib.request.build_opener(
        NoRedirect(), urllib.request.HTTPSHandler(context=ssl.create_default_context())
    )


def request(base, method, record, expected_source, allowed_allocations):
    """Return receipt metadata only; never retain service error bodies."""
    if not SAFE_VERSION.fullmatch(expected_source) or not allowed_allocations or any(
        not ALLOCATION_ID.fullmatch(item) for item in allowed_allocations
    ):
        raise ValueError("request provenance contract is invalid")
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
            provenance_ok = (
                isinstance(allocation, str)
                and ALLOCATION_ID.fullmatch(allocation) is not None
                and allocation in allowed_allocations
                and source == expected_source
            )
            return {
                "ok": response.status == 200 and data.get("id") == record and provenance_ok,
                "ms": (time.monotonic() - start) * 1000,
                "allocation": allocation if isinstance(allocation, str) and ALLOCATION_ID.fullmatch(allocation) else "",
                "version": source if isinstance(source, str) and SAFE_VERSION.fullmatch(source) else "",
                "id": record,
            }
    except (OSError, ValueError, urllib.error.HTTPError):
        return {"ok": False, "ms": (time.monotonic() - start) * 1000, "id": record}


def version(base, expected_source, allowed_allocations):
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
            if source != expected_source or allocation not in allowed_allocations:
                raise ValueError("version provenance mismatch")
            return {
                "allocation": allocation if isinstance(allocation, str) and ALLOCATION_ID.fullmatch(allocation) else "",
                "version": source if isinstance(source, str) and SAFE_VERSION.fullmatch(source) else "",
            }
    except (OSError, ValueError, urllib.error.HTTPError):
        return {"allocation": "", "version": ""}


def stop_pilot_allocation(allocation, namespace):
    """Stop only a caller-selected app allocation, after binding it to this job."""
    if not ALLOCATION_ID.fullmatch(allocation) or not PILOT_NAMESPACE.fullmatch(namespace) or not shutil.which("nomad"):
        return False
    try:
        inspect = subprocess.run(
            ["nomad", "alloc", "status", "-namespace=" + namespace, "-json", allocation],
            check=False, capture_output=True, text=True, timeout=15,
        )
        if inspect.returncode != 0 or len(inspect.stdout) > MAX_RESPONSE_BYTES:
            return False
        body = json.loads(inspect.stdout)
        job_id = body.get("JobID") if isinstance(body, dict) else ""
        inspected_namespace = body.get("Namespace") if isinstance(body, dict) else ""
        if job_id != "hello-norn-mysql" or inspected_namespace != namespace:
            return False
        stopped = subprocess.run(
            ["nomad", "alloc", "stop", "-namespace=" + namespace, "-yes", allocation],
            check=False, capture_output=True, text=True, timeout=30,
        )
        return stopped.returncode == 0
    except (OSError, ValueError, subprocess.TimeoutExpired):
        return False


def nomad_json(arguments):
    """Run one bounded Nomad read without retaining or reporting stderr."""
    if not shutil.which("nomad"):
        return None
    try:
        result = subprocess.run(["nomad", *arguments], check=False, capture_output=True, text=True, timeout=15)
        if result.returncode != 0 or len(result.stdout) > MAX_RESPONSE_BYTES:
            return None
        value = json.loads(result.stdout)
        return value
    except (OSError, ValueError, subprocess.TimeoutExpired):
        return None


def exercise_load(base, run_id, rps, seconds, workers, written, expected_source, allowed_allocations):
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
                batch.append((method, pool.submit(
                    request, base, method, record, expected_source, allowed_allocations
                )))
            for method, future in batch:
                row = future.result()
                rows.append(row)
                if method == "PUT" and row["ok"]:
                    written.append(row["id"])
            if len(rows) >= 50 and sum(not row["ok"] for row in rows[-50:]) >= 10:
                aborted = True
                break
    return rows, aborted


def verify_writes(base, records, workers, expected_source, allowed_allocations):
    """Bound post-run readback so acknowledged writes remain durable evidence."""
    verified = []
    deadline = time.monotonic() + 60
    with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as pool:
        for offset in range(0, len(records), workers):
            if time.monotonic() >= deadline:
                verified.extend({"id": item, "ok": False} for item in records[offset:])
                break
            verified.extend(pool.map(
                lambda item: request(base, "GET", item, expected_source, allowed_allocations),
                records[offset:offset + workers],
            ))
    return verified


def proof(base, receipt, expected_source, allowed_allocations):
    write = request(base, "PUT", receipt, expected_source, allowed_allocations)
    read = request(base, "GET", receipt, expected_source, allowed_allocations) if write["ok"] else {"ok": False}
    return {
        "id": receipt,
        "write": bool(write["ok"]),
        "read": bool(read["ok"]),
        "allocation": read.get("allocation", ""),
        "version": read.get("version", ""),
    }


def inventory_pilot(expected_image, expected_source, expected_hostname, expected_namespace):
    """Prove exactly two running, distinct ingress nodes for the reviewed job."""
    if not PILOT_NAMESPACE.fullmatch(expected_namespace):
        return [], False
    listed = nomad_json(["job", "allocs", "-namespace=" + expected_namespace, "-json", "hello-norn-mysql"])
    if not isinstance(listed, list):
        return [], False
    running = [item for item in listed if isinstance(item, dict) and item.get("ClientStatus") == "running"]
    if len(running) != 2:
        return [], False
    proofs = []
    for item in sorted(running, key=lambda value: value.get("ID", "")):
        allocation = item.get("ID", "")
        if not ALLOCATION_ID.fullmatch(allocation):
            return proofs, False
        body = nomad_json(["alloc", "status", "-namespace=" + expected_namespace, "-json", allocation])
        if not isinstance(body, dict):
            return proofs, False
        job = body.get("Job", {})
        meta = job.get("Meta", {}) if isinstance(job, dict) else {}
        image = meta.get("pilot_image", "") if isinstance(meta, dict) else ""
        source = meta.get("pilot_source_version", "") if isinstance(meta, dict) else ""
        hostname = meta.get("pilot_hostname", "") if isinstance(meta, dict) else ""
        node_id = body.get("NodeID", "")
        node = nomad_json(["node", "status", "-json", node_id]) if isinstance(node_id, str) else None
        if (body.get("JobID") != "hello-norn-mysql" or body.get("ClientStatus") != "running" or
                body.get("Namespace") != expected_namespace or not isinstance(node, dict) or
                node.get("NodePool") != "ingress" or image != expected_image or
                source != expected_source or hostname != expected_hostname):
            return proofs, False
        proofs.append({
            "allocation": allocation,
            "job": "hello-norn-mysql",
            "namespace": expected_namespace,
            "nodeID": node_id,
            "nodePool": "ingress",
            "clientStatus": "running",
            "image": image,
            "sourceVersion": source,
            "hostname": hostname,
            "createIndex": body.get("CreateIndex", 0),
            "modifyIndex": body.get("ModifyIndex", 0),
        })
    if len({item["nodeID"] for item in proofs}) != 2:
        return proofs, False
    return proofs, True


def wait_for_recovery(target, initial_ids, expected_image, expected_source, expected_hostname, namespace, seconds):
    """Require a stopped target and a two-node running inventory with a replacement."""
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        target_status = nomad_json(["alloc", "status", "-namespace=" + namespace, "-json", target])
        proofs, healthy = inventory_pilot(expected_image, expected_source, expected_hostname, namespace)
        current_ids = {item["allocation"] for item in proofs}
        replacements = sorted(current_ids - initial_ids)
        if healthy and isinstance(target_status, dict) and target_status.get("ClientStatus") != "running" and replacements:
            return proofs, replacements, True
        time.sleep(1)
    return [], [], False


def wait_for_public_replacement(base, receipt, replacements, expected_source, seconds):
    """Require the public record route to serve from a verified replacement."""
    deadline = time.monotonic() + min(seconds, 60)
    allowed = set(replacements)
    while time.monotonic() < deadline:
        row = request(base, "GET", receipt, expected_source, allowed)
        if row["ok"] and row.get("allocation") in allowed:
            return row["allocation"], True
        time.sleep(1)
    return "", False


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
    parser.add_argument("--expected-image", required=True)
    parser.add_argument("--expected-source-version", required=True)
    parser.add_argument("--expected-hostname", required=True)
    parser.add_argument("--namespace", required=True, help="exact run-scoped norn-pilot-* Nomad namespace")
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
    if not OCI_DIGEST.fullmatch(args.expected_image) or not SAFE_VERSION.fullmatch(args.expected_source_version):
        parser.error("expected image must be a lowercase OCI digest and source version must be a safe revision")
    if not PILOT_NAMESPACE.fullmatch(args.namespace):
        parser.error("namespace must be the exact run-scoped norn-pilot-* namespace")
    if urllib.parse.urlsplit("//" + args.expected_hostname).hostname != args.expected_hostname.lower():
        parser.error("expected hostname must be a hostname without a scheme or port")
    if url.hostname.lower() != args.expected_hostname.lower():
        parser.error("URL origin hostname must exactly match --expected-hostname")

    base = args.url.rstrip("/")
    run_id = uuid.uuid4().hex
    started_at = datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")
    started = time.monotonic()
    topology, topology_verified = inventory_pilot(args.expected_image, args.expected_source_version, args.expected_hostname, args.namespace)
    initial_ids = {item["allocation"] for item in topology}
    if not topology_verified:
        initial_ids = set()
    pre = proof(base, f"{run_id}-pre", args.expected_source_version, initial_ids)
    written = [pre["id"]] if pre["write"] and pre["read"] else []
    allowed_ids = initial_ids
    fault = {"requested": bool(args.fault_allocation), "target": args.fault_allocation or "", "stopAccepted": False, "targetStopped": not bool(args.fault_allocation), "recovered": not bool(args.fault_allocation), "replacementAllocations": [], "publicReplacementAllocation": ""}
    if args.fault_allocation:
        fault["stopAccepted"] = args.fault_allocation in initial_ids and stop_pilot_allocation(args.fault_allocation, args.namespace)
        if fault["stopAccepted"]:
            topology, replacements, topology_recovered = wait_for_recovery(args.fault_allocation, initial_ids, args.expected_image, args.expected_source_version, args.expected_hostname, args.namespace, args.recovery_seconds)
            fault["replacementAllocations"] = replacements
            fault["targetStopped"] = topology_recovered
            topology_verified = topology_recovered
            allowed_ids = {item["allocation"] for item in topology} if topology_recovered else set()
            if topology_recovered and written:
                replacement, public_recovered = wait_for_public_replacement(
                    base, pre["id"], replacements, args.expected_source_version, args.recovery_seconds
                )
                fault["publicReplacementAllocation"] = replacement
                fault["recovered"] = public_recovered

    rows, aborted = exercise_load(
        base, run_id, args.rps, args.seconds, args.workers, written,
        args.expected_source_version, allowed_ids,
    )
    post = proof(base, f"{run_id}-post", args.expected_source_version, allowed_ids)
    if post["write"] and post["read"]:
        written.append(post["id"])
    verification = verify_writes(base, written, args.workers, args.expected_source_version, allowed_ids)
    errors = sum(not row["ok"] for row in rows)
    availability = (len(rows) - errors) / len(rows) if rows else 0
    unverified = sum(not row["ok"] for row in verification)
    sources = sorted({row.get("version") for row in rows + verification if SAFE_VERSION.fullmatch(row.get("version", ""))})
    sources.extend(item.get("version") for item in (pre, post) if SAFE_VERSION.fullmatch(item.get("version", "")))
    sources = sorted(set(sources))
    source_verified = bool(sources) and set(sources) == {args.expected_source_version}
    report = {
        "runId": run_id,
        "origin": base,
        "startedAt": started_at,
        "endedAt": datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z"),
        "requestedLoad": {"rps": args.rps, "seconds": args.seconds, "workers": args.workers, "minAvailability": args.min_availability},
        "expectedArtifact": {"image": args.expected_image, "sourceVersion": args.expected_source_version, "hostname": args.expected_hostname, "namespace": args.namespace},
        "requests": len(rows),
        "elapsedSeconds": round(time.monotonic() - started, 2),
        "availability": round(availability, 4),
        "errors": errors,
        "abortedForErrorRate": aborted,
        "p50Ms": percentile(rows, .5),
        "p95Ms": percentile(rows, .95),
        "p99Ms": percentile(rows, .99),
        "allocations": [item["allocation"] for item in topology],
        "observedSourceVersions": sources,
        "nomadAllocations": topology,
        "nomadEvidenceVerified": topology_verified,
        "sourceVersionMatchesNomad": source_verified,
        "preDatabaseProof": pre,
        "postDatabaseProof": post,
        "acknowledgedWriteCount": len(written),
        "unverifiedWriteCount": unverified,
        "fault": fault,
    }
    print(json.dumps(report, indent=2, sort_keys=True))
    return 0 if (rows and availability >= args.min_availability and pre["read"] and post["read"] and not unverified and fault["recovered"] and topology_verified and source_verified) else 1


if __name__ == "__main__":
    raise SystemExit(main())
