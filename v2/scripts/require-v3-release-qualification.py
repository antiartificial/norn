#!/usr/bin/env python3
"""Require a successful protected-master v3 CI attempt for an exact release SHA."""

import json
import os
import re
import sys
import urllib.error
import urllib.request

EXPECTED_JOBS = {
    "api", "cli", "M7 mobility fixture image",
    "M4 durable replicas on three Nomad clients",
    "M1 migration process-crash recovery",
    "M1/M4 etcd deployment process-crash containment",
    "M4 private ingress transport", "M6 local mobility transfer and fence",
    "api production etcd TLS RBAC", "Mini synthetic schema fixture",
    "release bundle rehearsal", "workflow validation",
}
WORKFLOW_PATH = ".github/workflows/v3-ci.yml"


def api_get(path, token):
    url = "https://api.github.com" + path
    request = urllib.request.Request(url, headers={
        "Accept": "application/vnd.github+json",
        "Authorization": "Bearer " + token,
        "X-GitHub-Api-Version": "2022-11-28",
    })
    with urllib.request.urlopen(request, timeout=30) as response:
        return json.load(response)


def require_qualification(repository, sha, run_id, token, get=api_get):
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository):
        raise ValueError("invalid repository identity")
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise ValueError("release SHA must be exact lowercase 40-character SHA")
    if not re.fullmatch(r"[1-9][0-9]*", run_id):
        raise ValueError("qualification run ID must be a positive decimal integer")
    base = "/repos/" + repository + "/actions"
    workflow = get(base + "/workflows/v3-ci.yml", token)
    run = get(base + "/runs/" + run_id, token)
    if not isinstance(workflow, dict) or not isinstance(workflow.get("id"), int):
        raise ValueError("could not identify v3 CI workflow")
    if workflow.get("path") != WORKFLOW_PATH or workflow.get("state") != "active":
        raise ValueError("v3 CI workflow identity or state differs")
    identity = {
        "id": int(run_id), "workflow_id": workflow["id"],
        "event": "workflow_dispatch", "head_branch": "master",
        "head_sha": sha, "status": "completed", "conclusion": "success",
    }
    if not isinstance(run, dict) or any(run.get(k) != v for k, v in identity.items()):
        raise ValueError("qualification run identity, source, or conclusion differs")
    if run.get("path", "").split("@", 1)[0] != WORKFLOW_PATH:
        raise ValueError("qualification run workflow path differs")
    if run.get("repository", {}).get("full_name") != repository:
        raise ValueError("qualification run repository differs")
    attempt = run.get("run_attempt")
    if type(attempt) is not int or attempt < 1:
        raise ValueError("qualification run attempt missing")
    jobs = []
    page = 1
    while True:
        payload = get(base + "/runs/" + run_id + "/attempts/" + str(attempt) +
                      "/jobs?per_page=100&page=" + str(page), token)
        if not isinstance(payload, dict) or type(payload.get("total_count")) is not int:
            raise ValueError("qualification jobs response malformed")
        batch = payload.get("jobs")
        if not isinstance(batch, list) or not batch or len(batch) > 100:
            raise ValueError("qualification jobs page missing or malformed")
        jobs.extend(batch)
        total = payload["total_count"]
        if len(jobs) == total:
            break
        if len(jobs) > total or len(batch) != 100 or page >= 100:
            raise ValueError("qualification jobs pagination inconsistent")
        page += 1
    names = [job.get("name") for job in jobs if isinstance(job, dict)]
    if len(names) != len(jobs) or len(names) != len(set(names)) or set(names) != EXPECTED_JOBS:
        raise ValueError("qualification job set missing, duplicated, or unexpected")
    if any(job.get("status") != "completed" or job.get("conclusion") != "success"
           or type(job.get("id")) is not int or job.get("run_id") != int(run_id)
           or job.get("run_attempt") != attempt
           for job in jobs):
        raise ValueError("qualification jobs not all completed successfully")
    # A rerun may have begun while its previous attempt's jobs were listed.
    # The selected attempt must still be the run's current successful attempt.
    current = get(base + "/runs/" + run_id, token)
    if not isinstance(current, dict) or any(current.get(k) != v for k, v in identity.items()) \
            or current.get("run_attempt") != attempt:
        raise ValueError("qualification run changed during verification")
    return attempt, len(jobs)


def main():
    try:
        attempt, count = require_qualification(
            os.environ["GITHUB_REPOSITORY"], os.environ["REQUESTED_SHA"],
            os.environ["QUALIFICATION_RUN_ID"], os.environ["GH_TOKEN"])
    except (KeyError, ValueError, urllib.error.URLError, TimeoutError) as exc:
        print("v3 release qualification refused: " + str(exc), file=sys.stderr)
        return 1
    print(f"Qualified exact release SHA with protected v3 CI attempt {attempt}; {count} jobs succeeded.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
