#!/usr/bin/env python3
"""Provision a private, versioned DigitalOcean Spaces repository without leaking its key."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile

import boto3
from botocore.config import Config
from botocore.exceptions import ClientError


def run_json(*args: str) -> object:
    completed = subprocess.run(
        args,
        check=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    return json.loads(completed.stdout)


def find_value(value: object, wanted: str) -> str:
    normalized_wanted = re.sub(r"[^a-z0-9]", "", wanted.lower())
    if isinstance(value, dict):
        for key, nested in value.items():
            if re.sub(r"[^a-z0-9]", "", str(key).lower()) == normalized_wanted:
                if isinstance(nested, str) and nested:
                    return nested
            try:
                return find_value(nested, wanted)
            except KeyError:
                pass
    elif isinstance(value, list):
        for nested in value:
            try:
                return find_value(nested, wanted)
            except KeyError:
                pass
    raise KeyError(wanted)


def load_env(path: Path) -> tuple[list[str], dict[str, str]]:
    lines = path.read_text().splitlines()
    values: dict[str, str] = {}
    for line in lines:
        if not line or line.lstrip().startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        values[key] = value
    return lines, values


def store_env(path: Path, lines: list[str], updates: dict[str, str]) -> None:
    remaining = dict(updates)
    rendered: list[str] = []
    for line in lines:
        if "=" in line and not line.lstrip().startswith("#"):
            key = line.split("=", 1)[0]
            if key in remaining:
                rendered.append(f"{key}={remaining.pop(key)}")
                continue
        rendered.append(line)
    rendered.extend(f"{key}={value}" for key, value in remaining.items())
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "w") as handle:
            handle.write("\n".join(rendered) + "\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
    except BaseException:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass
        raise


def client(region: str, access_key: str, secret_key: str):
    return boto3.client(
        "s3",
        region_name=region,
        endpoint_url=f"https://{region}.digitaloceanspaces.com",
        aws_access_key_id=access_key,
        aws_secret_access_key=secret_key,
        config=Config(signature_version="s3v4", s3={"addressing_style": "virtual"}),
    )


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "command",
        choices=("init", "reinit-missing", "status", "prove-versioning", "cleanup-empty", "cleanup-destroyed"),
    )
    parser.add_argument("--lab-name", required=True)
    parser.add_argument("--secret-file", required=True, type=Path)
    parser.add_argument("--region", default="tor1")
    parser.add_argument("--bucket")
    parser.add_argument("--confirm-disposable-lab", action="store_true")
    args = parser.parse_args()

    lines, env = load_env(args.secret_file)
    if args.command == "cleanup-empty":
        if not args.bucket or not re.fullmatch(rf"{re.escape(args.lab_name)}-backups-[a-f0-9]{{10}}", args.bucket):
            raise SystemExit("refusing cleanup outside this lab's generated empty-bucket namespace")
        key_name = f"{args.lab_name}-cleanup-{os.urandom(4).hex()}"
        created = run_json(
            "doctl", "spaces", "keys", "create", key_name,
            "--grants", "bucket=;permission=fullaccess", "--output", "json",
        )
        access_key = find_value(created, "accesskey")
        secret_key = find_value(created, "secretkey")
        try:
            s3 = client(args.region, access_key, secret_key)
            versions = s3.list_object_versions(Bucket=args.bucket)
            if versions.get("Versions") or versions.get("DeleteMarkers"):
                raise SystemExit("refusing to delete a non-empty backup bucket")
            s3.delete_bucket(Bucket=args.bucket)
        finally:
            subprocess.run(
                ("doctl", "spaces", "keys", "delete", access_key),
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
            )
        print(json.dumps({"bucket": args.bucket, "deleted": True}))
        return
    bucket = env.get("NORN_HA_BACKUP_BUCKET", "")
    access_key = env.get("NORN_HA_BACKUP_ACCESS_KEY", "")
    secret_key = env.get("NORN_HA_BACKUP_SECRET_KEY", "")

    if args.command == "cleanup-destroyed":
        if not args.confirm_disposable_lab:
            raise SystemExit("refusing cleanup: pass --confirm-disposable-lab after Terraform destroy")
        if not bucket or not re.fullmatch(rf"{re.escape(args.lab_name)}-backups-[a-f0-9]{{10}}", bucket):
            raise SystemExit("refusing cleanup outside this lab's generated backup namespace")
        if not (access_key and secret_key):
            raise SystemExit("cleanup requires the complete scoped backup credential")

        region = env.get("NORN_HA_BACKUP_REGION", args.region)
        scoped = client(region, access_key, secret_key)
        state_key = f"terraform/state/{args.lab_name}.tfstate"
        try:
            state = json.loads(scoped.get_object(Bucket=bucket, Key=state_key)["Body"].read())
        except (ClientError, json.JSONDecodeError) as error:
            raise SystemExit("refusing cleanup: current remote Terraform state is unreadable") from error
        if state.get("version") != 4 or state.get("resources") != []:
            raise SystemExit("refusing cleanup: remote Terraform state still contains resources")

        bootstrap_name = f"{args.lab_name}-cleanup-{os.urandom(4).hex()}"
        created = run_json(
            "doctl", "spaces", "keys", "create", bootstrap_name,
            "--grants", "bucket=;permission=fullaccess", "--output", "json",
        )
        bootstrap_access_key = find_value(created, "accesskey")
        bootstrap_secret_key = find_value(created, "secretkey")
        deleted_versions = 0
        try:
            full = client(region, bootstrap_access_key, bootstrap_secret_key)
            paginator = full.get_paginator("list_object_versions")
            for page in paginator.paginate(Bucket=bucket):
                objects = [
                    {"Key": item["Key"], "VersionId": item["VersionId"]}
                    for item in page.get("Versions", []) + page.get("DeleteMarkers", [])
                ]
                for start in range(0, len(objects), 1000):
                    batch = objects[start:start + 1000]
                    if batch:
                        response = full.delete_objects(Bucket=bucket, Delete={"Objects": batch, "Quiet": True})
                        if response.get("Errors"):
                            raise RuntimeError("one or more backup object versions could not be deleted")
                        deleted_versions += len(batch)
            remaining = full.list_object_versions(Bucket=bucket)
            if remaining.get("Versions") or remaining.get("DeleteMarkers"):
                raise RuntimeError("backup bucket is not empty after version cleanup")
            full.delete_bucket(Bucket=bucket)
            subprocess.run(
                ("doctl", "spaces", "keys", "delete", access_key),
                check=True,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.PIPE,
            )
            store_env(
                args.secret_file,
                lines,
                {
                    "NORN_HA_BACKUP_BUCKET": "",
                    "NORN_HA_BACKUP_REGION": "",
                    "NORN_HA_BACKUP_ENDPOINT": "",
                    "NORN_HA_BACKUP_ACCESS_KEY": "",
                    "NORN_HA_BACKUP_SECRET_KEY": "",
                },
            )
        finally:
            subprocess.run(
                ("doctl", "spaces", "keys", "delete", bootstrap_access_key),
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
            )
        print(json.dumps({"bucket": bucket, "deleted": True, "deleted_versions": deleted_versions}))
        return

    should_initialize = args.command == "init" and not (bucket and access_key and secret_key)
    should_reinitialize = args.command == "reinit-missing"
    if should_initialize or should_reinitialize:
        if should_initialize and any((bucket, access_key, secret_key)):
            raise SystemExit("refusing partial backup credential state; reconcile it manually")
        if should_reinitialize and not all((bucket, access_key, secret_key)):
            raise SystemExit("reinit-missing requires complete stale backup configuration")

        stale_bucket = bucket if should_reinitialize else ""
        stale_access_key = access_key if should_reinitialize else ""
        suffix = os.urandom(5).hex()
        new_bucket = f"{args.lab_name}-backups-{suffix}"
        key_name = f"{args.lab_name}-backup-{suffix}"
        bootstrap_name = f"{args.lab_name}-bootstrap-{suffix}"
        created = run_json(
            "doctl", "spaces", "keys", "create", bootstrap_name,
            "--grants", "bucket=;permission=fullaccess", "--output", "json",
        )
        bootstrap_access_key = ""
        bootstrap_secret_key = ""
        scoped_access_key = ""
        try:
            bootstrap_access_key = find_value(created, "accesskey")
            bootstrap_secret_key = find_value(created, "secretkey")
            s3 = client(args.region, bootstrap_access_key, bootstrap_secret_key)
            if stale_bucket:
                try:
                    s3.head_bucket(Bucket=stale_bucket)
                except ClientError as error:
                    code = error.response.get("Error", {}).get("Code", "")
                    status = error.response.get("ResponseMetadata", {}).get("HTTPStatusCode")
                    if code not in ("NoSuchBucket", "404") and status != 404:
                        raise
                else:
                    raise SystemExit(f"refusing to replace existing backup bucket {stale_bucket}")

            s3.create_bucket(Bucket=new_bucket, ACL="private")
            s3.put_bucket_versioning(
                Bucket=new_bucket,
                VersioningConfiguration={"Status": "Enabled"},
            )
            acl = s3.get_bucket_acl(Bucket=new_bucket)
            public_grants = [
                grant for grant in acl.get("Grants", [])
                if grant.get("Grantee", {}).get("URI", "").endswith(("AllUsers", "AuthenticatedUsers"))
            ]
            if public_grants:
                raise RuntimeError("new backup bucket has a public ACL grant")
            scoped = run_json(
                "doctl", "spaces", "keys", "create", key_name,
                "--grants", f"bucket={new_bucket};permission=readwrite", "--output", "json",
            )
            scoped_access_key = find_value(scoped, "accesskey")
            access_key = scoped_access_key
            secret_key = find_value(scoped, "secretkey")
            store_env(
                args.secret_file,
                lines,
                {
                    "NORN_HA_BACKUP_BUCKET": new_bucket,
                    "NORN_HA_BACKUP_REGION": args.region,
                    "NORN_HA_BACKUP_ENDPOINT": f"{args.region}.digitaloceanspaces.com",
                    "NORN_HA_BACKUP_ACCESS_KEY": access_key,
                    "NORN_HA_BACKUP_SECRET_KEY": secret_key,
                },
            )
            subprocess.run(
                ("doctl", "spaces", "keys", "delete", bootstrap_access_key),
                check=True,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.PIPE,
            )
            if stale_access_key and stale_access_key != scoped_access_key:
                subprocess.run(
                    ("doctl", "spaces", "keys", "delete", stale_access_key),
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL,
                )
            bucket = new_bucket
        except BaseException:
            if bootstrap_access_key and new_bucket:
                try:
                    client(args.region, bootstrap_access_key, bootstrap_secret_key).delete_bucket(Bucket=new_bucket)
                except BaseException:
                    pass
            for doomed_key in (scoped_access_key, bootstrap_access_key):
                if doomed_key:
                    subprocess.run(
                        ("doctl", "spaces", "keys", "delete", doomed_key),
                        stdout=subprocess.DEVNULL,
                        stderr=subprocess.DEVNULL,
                    )
            raise

    if not (bucket and access_key and secret_key):
        raise SystemExit("backup repository is not initialized")
    s3 = client(env.get("NORN_HA_BACKUP_REGION", args.region), access_key, secret_key)
    versioning = s3.get_bucket_versioning(Bucket=bucket).get("Status")
    if versioning != "Enabled":
        raise SystemExit("backup bucket versioning is not enabled")
    if args.command == "prove-versioning":
        key = f"norn-evidence/versioning-{os.urandom(8).hex()}"
        first = s3.put_object(Bucket=bucket, Key=key, Body=b"first")
        second = s3.put_object(Bucket=bucket, Key=key, Body=b"second")
        versions = s3.list_object_versions(Bucket=bucket, Prefix=key).get("Versions", [])
        version_ids = [item["VersionId"] for item in versions if item.get("Key") == key]
        try:
            if len(version_ids) < 2 or first.get("VersionId") == second.get("VersionId"):
                raise SystemExit("versioned overwrite did not produce two distinct object versions")
            print(json.dumps({
                "bucket": bucket,
                "key": key,
                "versions": len(version_ids),
                "latest_version": second.get("VersionId"),
            }))
        finally:
            for version_id in version_ids:
                s3.delete_object(Bucket=bucket, Key=key, VersionId=version_id)
        return
    try:
        acl = s3.get_bucket_acl(Bucket=bucket)
    except ClientError as error:
        if error.response["Error"]["Code"] != "AccessDenied":
            raise SystemExit(f"backup bucket access check failed: {error.response['Error']['Code']}") from error
        acl_private = None
    else:
        public_grants = [
            grant for grant in acl.get("Grants", [])
            if grant.get("Grantee", {}).get("URI", "").endswith(("AllUsers", "AuthenticatedUsers"))
        ]
        if public_grants:
            raise SystemExit("backup bucket has a public ACL grant")
        acl_private = True
    print(json.dumps({
        "bucket": bucket,
        "region": env.get("NORN_HA_BACKUP_REGION", args.region),
        "private_acl_visible": acl_private,
        "scoped_key": True,
        "versioning": versioning,
    }))


if __name__ == "__main__":
    main()
