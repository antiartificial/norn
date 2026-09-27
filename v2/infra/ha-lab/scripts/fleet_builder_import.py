#!/usr/bin/env python3
"""Import only the Fleet Builder topology the co-located HA lab can express."""

import json
import os
import re
import stat
import sys
import tempfile
from pathlib import Path


def require(condition, message):
    if not condition:
        raise ValueError(message)


def slug(value, field):
    require(isinstance(value, str) and re.fullmatch(r"[a-z0-9][a-z0-9-]*", value), f"{field} must be a lowercase slug")
    return value


def values(draft):
    require(isinstance(draft, dict), "Fleet Builder input must be an object")
    name = slug(draft.get("name"), "name")
    region = slug(draft.get("region"), "region")
    sizes = draft.get("sizes")
    require(isinstance(sizes, dict), "sizes is required")
    node_size = slug(sizes.get("control"), "sizes.control")
    require(sizes.get("app") == node_size, "lab nodes have one size; sizes.app must equal sizes.control")
    count = draft.get("controlA")
    require(type(count) is int and count >= 3 and count % 2 == 1, "controlA must be odd and at least 3")
    require(draft.get("regions") == 1 and draft.get("controlB") == 0, "lab supports one region and no second control pool")
    require(draft.get("edge") == "none", "lab import does not support a builder edge")
    require(draft.get("appA") == 2 and draft.get("appB") == 2, "lab import does not support separate app pools")
    require(draft.get("services") == [] and draft.get("extras") == [], "lab import does not support builder services or extra databases")
    db = draft.get("db")
    require(isinstance(db, dict), "db is required")
    require(db.get("mode") in ("self", "managed") and db.get("engine") == "pg", "lab supports self or managed PostgreSQL")
    require(db.get("replica") is False and db.get("replicaRegion") == "same", "lab import does not support a separate database replica")
    db_size = slug(db.get("managedSize"), "db.managedSize")
    require(db.get("selfSize") == node_size, "lab self-hosted database uses the same node size")
    caches = draft.get("managedValkey")
    require(isinstance(caches, list) and len(caches) <= 1, "lab supports at most one managed Valkey")
    result = {
        "name_prefix": name, "region": region, "node_count": count,
        "node_size": node_size, "use_managed_db": db["mode"] == "managed",
        "db_size": db_size, "use_managed_redis": bool(caches),
    }
    if caches:
        cache = caches[0]
        require(isinstance(cache, dict) and cache.get("name") == name + "-valkey" and cache.get("region") == region, "managed Valkey must use the lab name and region")
        result["redis_size"] = slug(cache.get("size"), "managedValkey.size")
    return result


def write_tfvars(path, updates):
    existing = path.exists()
    if existing:
        lines = path.read_text().splitlines(keepends=True)
        mode = stat.S_IMODE(path.stat().st_mode)
    else:
        lines = [
            'owner = "platform"\n', 'expires_on = "REPLACE-YYYY-MM-DD"\n',
            'ssh_key_fingerprints = ["REPLACE-with-doctl-ssh-key-fingerprint"]\n',
            'ssh_source_cidrs = ["REPLACE-your.ip/32"]\n',
        ]
        mode = 0o600
    keys = set(updates)
    seen = set()
    rewritten = []
    for line in lines:
        match = re.match(r"^(\s*)([a-z_]+)(\s*=).*$", line)
        if match and match.group(2) in keys:
            key = match.group(2)
            require(key not in seen, f"duplicate {key} in terraform.tfvars")
            seen.add(key)
            rewritten.append(f"{match.group(1)}{key}{match.group(3)} {json.dumps(updates[key])}\n")
        else:
            rewritten.append(line)
    for key, value in updates.items():
        if key not in seen:
            rewritten.append(f"{key} = {json.dumps(value)}\n")
    descriptor, temp_name = tempfile.mkstemp(prefix=".terraform.tfvars.", dir=path.parent)
    try:
        os.fchmod(descriptor, mode)
        with os.fdopen(descriptor, "w") as output:
            output.writelines(rewritten)
        os.replace(temp_name, path)
    finally:
        if os.path.exists(temp_name):
            os.unlink(temp_name)
    print(f"{'updated' if existing else 'wrote'} {path}; review before lab up")


if __name__ == "__main__":
    try:
        source, destination = map(Path, sys.argv[1:3])
        write_tfvars(destination, values(json.loads(source.read_text())))
    except (ValueError, OSError, json.JSONDecodeError) as error:
        print(f"fleet import: {error}", file=sys.stderr)
        sys.exit(1)
