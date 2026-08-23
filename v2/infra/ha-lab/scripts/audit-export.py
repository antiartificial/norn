#!/usr/bin/env python3
"""Upload a value-safe Norn mutation-audit snapshot to a versioned Space."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import sys
from datetime import datetime, timezone

import boto3
from botocore.config import Config


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--lab-name", required=True)
    parser.add_argument("--bucket", required=True)
    parser.add_argument("--region", required=True)
    args = parser.parse_args()

    access_key = os.environ.get("NORN_HA_BACKUP_ACCESS_KEY", "")
    secret_key = os.environ.get("NORN_HA_BACKUP_SECRET_KEY", "")
    if not access_key or not secret_key:
        raise SystemExit("backup repository credentials are not configured")

    audit = json.load(sys.stdin)
    if audit.get("schema") != "norn.mutation-audit/v1" or not isinstance(audit.get("events"), list):
        raise SystemExit("refusing an unexpected mutation-audit response")
    invalid = [event.get("id", "unknown") for event in audit["events"] if event.get("integrity") == "invalid"]
    if invalid:
        raise SystemExit(f"refusing to export {len(invalid)} invalid audit receipt(s)")

    now = datetime.now(timezone.utc)
    envelope = {
        "schema": "norn.mutation-audit-export/v1",
        "exportedAt": now.isoformat().replace("+00:00", "Z"),
        "source": args.lab_name,
        "count": len(audit["events"]),
        "audit": audit,
    }
    payload = (json.dumps(envelope, sort_keys=True, separators=(",", ":")) + "\n").encode()
    digest = hashlib.sha256(payload).hexdigest()
    key = f"audit/{now:%Y/%m/%d}/{now:%Y%m%dT%H%M%SZ}-{digest[:12]}.json"

    client = boto3.client(
        "s3",
        region_name=args.region,
        endpoint_url=f"https://{args.region}.digitaloceanspaces.com",
        aws_access_key_id=access_key,
        aws_secret_access_key=secret_key,
        config=Config(signature_version="s3v4", s3={"addressing_style": "virtual"}),
    )
    if client.get_bucket_versioning(Bucket=args.bucket).get("Status") != "Enabled":
        raise SystemExit("refusing audit export because bucket versioning is not enabled")
    uploaded = client.put_object(
        Bucket=args.bucket,
        Key=key,
        Body=payload,
        ContentType="application/json",
        Metadata={"sha256": digest, "schema": "norn.mutation-audit-export-v1"},
    )
    sidecar_key = key + ".sha256"
    sidecar = client.put_object(
        Bucket=args.bucket,
        Key=sidecar_key,
        Body=f"{digest}  {key}\n".encode(),
        ContentType="text/plain",
    )
    recovered = client.get_object(Bucket=args.bucket, Key=key)["Body"].read()
    if hashlib.sha256(recovered).hexdigest() != digest:
        raise SystemExit("uploaded audit export checksum verification failed")

    print(json.dumps({
        "schema": "norn.ha-drill/v1",
        "kind": "audit.offhost-export",
        "bucket": args.bucket,
        "key": key,
        "version": uploaded.get("VersionId"),
        "checksumKey": sidecar_key,
        "checksumVersion": sidecar.get("VersionId"),
        "sha256": digest,
        "events": len(audit["events"]),
        "verified": True,
        "worm": False,
    }, sort_keys=True))


if __name__ == "__main__":
    main()
