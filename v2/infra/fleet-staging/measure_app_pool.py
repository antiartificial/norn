#!/usr/bin/env python3
"""Read-only, value-safe evidence for one stage of work-staging app-pool scaling."""
import argparse
import datetime
import json
import re
import subprocess


NODE_ID = re.compile(r"^[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$")
NAMESPACE = "norn-work-staging"
JOB = "hello-norn-mysql"


def nomad_json(arguments):
    result = subprocess.run(["nomad", *arguments], check=True, capture_output=True, text=True, timeout=20)
    return json.loads(result.stdout)


def measure(nodes, allocations, details, expected_count):
    """Reject unknown/partial topology instead of silently counting it as fit."""
    if not isinstance(nodes, list) or not isinstance(allocations, list) or not isinstance(details, list):
        raise ValueError("Nomad inventory is not a list")
    app = [n for n in nodes if isinstance(n, dict) and n.get("NodePool") == "app"]
    ready = [n for n in app if n.get("Status") == "ready" and n.get("SchedulingEligibility") == "eligible"]
    app_ids = [n.get("ID") for n in app]
    ready_ids = [n.get("ID") for n in ready]
    if (len(app) != expected_count or len(ready) != expected_count or
            len(set(app_ids)) != expected_count or
            any(not isinstance(i, str) or not NODE_ID.fullmatch(i) for i in app_ids)):
        raise ValueError("app pool count, readiness, eligibility, or node identity differs")
    running = [a for a in allocations if isinstance(a, dict) and a.get("ClientStatus") == "running"]
    if (len(running) != 2 or len({a.get("NodeID") for a in running}) != 2 or
            any(not isinstance(a.get("ID"), str) or not NODE_ID.fullmatch(a["ID"]) for a in running) or
            any(a.get("NodeID") not in ready_ids for a in running)):
        raise ValueError("two running workload allocations must occupy distinct ready app nodes")
    by_id = {a["ID"]: a for a in running}
    if (len(details) != 2 or any(not isinstance(d, dict) or not isinstance(d.get("ID"), str) or d["ID"] not in by_id for d in details) or
            len({d["ID"] for d in details}) != 2):
        raise ValueError("allocation detail inventory is incomplete")
    for detail in details:
        task = detail.get("TaskStates", {}).get("web") if isinstance(detail.get("TaskStates"), dict) else None
        deployment = detail.get("DeploymentStatus")
        job = detail.get("Job")
        if (detail.get("JobID") != JOB or detail.get("Namespace") != NAMESPACE or
                detail.get("ClientStatus") != "running" or detail.get("DesiredStatus") != "run" or
                detail.get("NodeID") != by_id[detail["ID"]]["NodeID"] or
                not isinstance(job, dict) or job.get("NodePool") != "app" or
                not isinstance(task, dict) or task.get("State") != "running" or
                not isinstance(deployment, dict) or deployment.get("Healthy") is not True):
            raise ValueError("allocation task or Nomad deployment health differs")
    return {
        "appNodeCount": expected_count,
        "appNodeIDs": sorted(app_ids),
        "readyEligibleAppNodeIDs": sorted(ready_ids),
        "nomadHealthyAllocationIDs": sorted(a["ID"] for a in running),
        "runningAllocationNodeIDs": sorted(a["NodeID"] for a in running),
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--phase", required=True, choices=("before", "expanded", "contracted"))
    parser.add_argument("--expected-count", required=True, type=int, choices=(2, 3))
    args = parser.parse_args()
    if (args.phase == "expanded") != (args.expected_count == 3):
        parser.error("expanded requires count 3; before/contracted require count 2")
    try:
        nodes = nomad_json(["node", "status", "-json"])
        allocations = nomad_json(["job", "allocs", "-namespace=" + NAMESPACE, "-json", JOB])
        if not isinstance(allocations, list):
            raise ValueError("job allocation inventory is not a list")
        running = [a for a in allocations if isinstance(a, dict) and a.get("ClientStatus") == "running"]
        if len(running) != 2:
            raise ValueError("expected exactly two running service allocations")
        details = [nomad_json(["alloc", "status", "-namespace=" + NAMESPACE, "-json", a["ID"]]) for a in running]
        evidence = measure(
            nodes, allocations, details,
            args.expected_count,
        )
    except (ValueError, KeyError, subprocess.SubprocessError, json.JSONDecodeError) as error:
        parser.exit(1, "app-pool measurement failed: " + str(error) + "\n")
    print(json.dumps({
        "phase": args.phase,
        "namespace": NAMESPACE,
        "job": JOB,
        "observedAt": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        **evidence,
    }, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
