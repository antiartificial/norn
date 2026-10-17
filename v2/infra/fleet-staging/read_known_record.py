#!/usr/bin/env python3
"""Read one pre-existing synthetic record after a staging topology change."""
import argparse
import json
import re
import urllib.parse

import exercise


RECORD = re.compile(r"^[a-f0-9]{32}-(?:pre|post|[0-9]+)$")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", required=True)
    parser.add_argument("--record", required=True, help="synthetic receipt ID from an earlier exercise JSON")
    parser.add_argument("--expected-image", required=True)
    parser.add_argument("--expected-source-version", required=True)
    parser.add_argument("--expected-hostname", required=True)
    parser.add_argument("--app-node-ids-json", required=True)
    args = parser.parse_args()
    parsed = urllib.parse.urlsplit(args.url)
    if (parsed.scheme != "https" or parsed.hostname != args.expected_hostname or parsed.path not in ("", "/") or
            parsed.query or parsed.fragment or parsed.username or parsed.password or not RECORD.fullmatch(args.record) or
            not exercise.OCI_DIGEST.fullmatch(args.expected_image) or
            not exercise.SAFE_VERSION.fullmatch(args.expected_source_version)):
        parser.error("origin, record, image or source version invalid")
    try:
        nodes = exercise.parse_app_node_ids(args.app_node_ids_json)
    except ValueError as error:
        parser.error(str(error))
    topology, healthy = exercise.inventory_staging(
        args.expected_image, args.expected_source_version, args.expected_hostname,
        "norn-work-staging", nodes,
    )
    allowed = {item["allocation"] for item in topology} if healthy else set()
    response = (exercise.request(args.url.rstrip("/"), "GET", args.record, args.expected_source_version, allowed)
                if allowed else {"ok": False})
    print(json.dumps({"record": args.record, "readVerified": bool(healthy and response["ok"]),
                      "allocation": response.get("allocation", ""), "nomadEvidenceVerified": healthy}, sort_keys=True))
    return 0 if healthy and response["ok"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
