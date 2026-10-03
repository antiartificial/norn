#!/usr/bin/env python3
"""Lint M8 ledger boundaries and current-worktree CI pin consistency."""

import json
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
LEDGER = ROOT / "docs/v3/m8-release-evidence-ledger.json"
WORKFLOW = ROOT / ".github/workflows/ci.yml"
SHA = re.compile(r"^[0-9a-f]{40}$")


def require(condition: bool, message: str) -> None:
    if not condition:
        raise SystemExit(f"M8 evidence ledger invalid: {message}")


def main() -> None:
    data = json.loads(LEDGER.read_text())
    norn = data["norn"]
    candidate = norn["signed_candidate"]
    source = norn["current_source"]
    mini = norn["mini_live"]
    fleet = data["fleet"]
    ui = data["nornui"]

    require(data["milestone"] == "M8" and data["milestone_status"] == "open",
            "this checkpoint must not declare M8 complete")
    for label, value in (
        ("candidate", candidate["commit"]),
        ("current source", source["commit"]),
        ("Mini", mini["commit"]),
        ("current protected Fleet source", fleet["current_protected_source"]["commit"]),
        ("NornUI observation", ui["last_observed_source_commit"]),
    ):
        require(bool(SHA.fullmatch(value)), f"{label} commit must be a full SHA")

    require(candidate["tag"] == f"platform-{candidate['commit']}",
            "candidate tag must bind the full candidate commit")
    require(candidate["evidence_class"] == "signed_runtime_candidate",
            "candidate must remain the signed runtime candidate")
    require(source["evidence_class"] == "protected_signed_source"
            and candidate["commit"] == source["commit"],
            "signed candidate must bind exact protected source")
    require(candidate["asset_count"] == 16
            and candidate["release_run"] == 36936156456
            and candidate["release_api_immutable"] is True
            and candidate["asset_bytes_sha256_verified"] is True
            and candidate["bundle_signatures_verified"] is True
            and candidate["mini_import_or_preflight"] == "not_repeated_for_this_candidate",
            "current release facts and unperformed Mini qualification boundaries must remain explicit")
    receipt = ROOT / candidate["asset_verification_receipt"]
    require(receipt.is_file(),
            "verified release assets require a dated non-secret receipt")
    receipt_text = receipt.read_text()
    for claim in (candidate["commit"], "all 16 release assets", "Ed25519"):
        require(claim in receipt_text,
                "release receipt must bind the candidate and verification claims")
    require(candidate["commit"] != mini["commit"],
            "signed candidate must not be presented as Mini live runtime")
    require(not mini["installed_digest_verified"] and not mini["fleet_configured"]
            and mini["production_readiness"] == "blocked_separately",
            "Mini digest/Fleet claims exceed the recorded observation")
    require((ROOT / mini["readback_receipt"]).is_file(),
            "Mini readback requires a dated source and boundary summary")
    pilot = fleet["pilot261002d"]
    require(pilot["status"] == "offline_hold_packet"
            and pilot["run_matching_provider_resources_observed"] == 0
            and pilot["run_matching_billable_resources_observed"] == 0,
            "current Fleet HOLD packet must remain zero for this run")
    require(fleet["live"]["evidence_class"] == "unobserved"
            and fleet["live"]["commit"] is None,
            "Fleet source and HOLD packet must not be presented as observed installed state")
    require(fleet["current_protected_source"]["runtime_pin_state"]
            == "unresolved_until_protected_plan"
            and len(fleet["current_protected_source"]["required_runtime_pins"]) == 5,
            "uncreated Fleet runtime pins must remain explicit and unresolved")
    require(ui["released_or_installed_version"] is None
            and ui["m8_support"] == "unknown_unsupported",
            "NornUI lacks exact release and runtime evidence")
    required_gates = {
        "coherent_signed_candidate",
        "exact_nornui_release_and_client_parity",
        "mini_private_upgrade_rollback_live_preservation",
        "m6_application_database_cutover_and_recovery",
        "m7_mini_to_fleet_application_mobility",
        "fleet_protected_plan_apply_installed_readback",
        "fleet_runner_and_upstream_version_binding",
        "fleet_fixture_topology_resource_binding",
        "fleet_request_error_latency_results",
        "fleet_operation_effect_reconciliation",
        "fleet_soak_fault_retention_restore_retirement",
        "operator_release_signoff",
    }
    gates = data["release_contract_gates"]
    require(set(gates) == required_gates,
            "release-contract gate set must remain exact")
    require(all(gate == {"status": "open", "evidence_refs": []}
                for gate in gates.values()),
            "no release-contract gate has evidence or acceptance at this checkpoint")

    workflow = WORKFLOW.read_text()
    for label, value in data["norn_ci_pins"].items():
        require(value in workflow,
                f"{label} pin {value!r} does not match ci.yml")

    print("M8 release evidence ledger lint: OK (milestone remains open)")


if __name__ == "__main__":
    main()
