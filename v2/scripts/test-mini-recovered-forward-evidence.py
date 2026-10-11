#!/usr/bin/env python3
"""Hermetic strict-parser and binding tests for recovered-forward evidence."""

import hashlib
import hmac
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("mini-recovered-forward-verify-evidence")
KEY = b"fixture-audit-signing-key-at-least-32-bytes"
SOURCE = "a" * 40
CANDIDATE = "b" * 40
INCIDENT_CANDIDATE = "c" * 40
TRANSITION = "fixture-transition-001"
INCIDENT_BEFORE = "9" * 64


def sha(raw: bytes) -> str:
    return hashlib.sha256(raw).hexdigest()


def strict_dump(value: dict) -> bytes:
    return (json.dumps(value, sort_keys=True, separators=(",", ":")) + "\n").encode()


class RecoveredForwardEvidenceTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.home = self.root / "home"
        (self.home / ".config/norn").mkdir(parents=True)
        self.env = os.environ.copy()
        self.env.update(HOME=str(self.home), NORN_AUDIT_SIGNING_KEY=KEY.decode())
        self.artifact = self.private("backup.dump", b"fixture database backup\n")
        self.proof = self.private("backup-proof.json", b"")
        self.shadow = self.private("shadow.json", b"")
        self.adoption_inputs = {}
        refs = {}
        fence = {"schema": "norn.legacy-baseline-fence/v1", "state": "candidate-postflight-failed",
                 "legacyReleaseSHA": "d" * 40}
        ledger = {"schema": "norn.m5-protected-transition/v1", "state": "candidate-recovery-required",
                  "legacyReleaseSHA": "d" * 40, "candidateSHA": INCIDENT_CANDIDATE,
                  "upgradeExitCode": 1, "beforeDigest": INCIDENT_BEFORE}
        reconciliation = {"schema": "norn.m5-post-transition-reconciliation/v1",
                          "release": {"sha": INCIDENT_CANDIDATE, "verified": True},
                          "transition": {"beforeDigest": INCIDENT_BEFORE,
                                         "ledgerState": "candidate-recovery-required",
                                         "matchesBefore": False, "upgradeExitCode": 1}}
        for label, value in (("fence", fence), ("ledger", ledger), ("reconciliation", reconciliation)):
            path = self.private(f"{label}.json", strict_dump(value))
            refs[label] = {"path": str(path), "sha256": sha(path.read_bytes())}
        adoption = {
            "schema": "norn.recovered-baseline-adoption/v2",
            "sourceSHA": SOURCE,
            "incidentCandidateSHA": INCIDENT_CANDIDATE,
            "adopterSHA": "a" * 40,
            "incidentBindingSHA256": "c" * 64,
            "preservationUnprovenReasonSHA256": "2" * 64,
            "legacyFence": refs["fence"],
            "ledger": refs["ledger"],
            "reconciliation": refs["reconciliation"],
            "preservationProven": False,
        }
        canonical = json.dumps(adoption, sort_keys=True, separators=(",", ":")).encode()
        adoption["signature"] = "hmac-sha256:" + hmac.new(KEY, b"norn.recovered-baseline-adoption/v2\0" + canonical, hashlib.sha256).hexdigest()
        self.adoption = self.private(self.home / ".config/norn/.norn-recovered-baseline-adoption-v2.json", strict_dump(adoption))
        self.refs = refs
        self.write_inputs()

    def private(self, name, raw: bytes) -> Path:
        path = Path(name) if isinstance(name, Path) else self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(raw)
        path.chmod(0o600)
        return path

    def write_inputs(self, proof_raw: bytes | None = None):
        artifact_raw = self.artifact.read_bytes()
        proof = {
            "schema": "norn.legacy-control-backup/v1",
            "sourceReleaseSHA": SOURCE,
            "databaseIdentity": "hmac-sha256:" + "3" * 64,
            "backupSHA256": sha(artifact_raw),
            "backupBytes": len(artifact_raw),
            "createdAt": "2026-10-11T12:00:00+00:00",
        }
        proof_raw = proof_raw or strict_dump(proof)
        self.proof.write_bytes(proof_raw)
        shadow = {
            "schema": "norn.m5-signed-shadow-launchagent-rehearsal/v2",
            "completedAt": "2026-10-11T12:01:00+00:00",
            "exitCode": 0,
            "candidateSHA": CANDIDATE,
            "shadowLabel": "fixture-shadow",
            "shadowPort": 18999,
            "runtimeClaim": "passed",
            "privateRestoreClaim": "passed",
            "backupSHA256": sha(artifact_raw),
            "backupBytes": len(artifact_raw),
        }
        shadow_raw = strict_dump(shadow)
        self.shadow.write_bytes(shadow_raw)
        value = {
            "schema": "norn.recovered-forward-evidence/v2",
            "transitionId": TRANSITION,
            "sourceSHA": SOURCE,
            "candidateSHA": CANDIDATE,
            "databaseIdentity": "hmac-sha256:" + "3" * 64,
            "recoveryFenceSHA256": self.refs["fence"]["sha256"],
            "adoptionReceiptSHA256": sha(self.adoption.read_bytes()),
            "incidentBindingSHA256": "c" * 64,
            "ledgerSHA256": self.refs["ledger"]["sha256"],
            "reconciliationSHA256": self.refs["reconciliation"]["sha256"],
            "backupProofSHA256": sha(proof_raw),
            "backupSHA256": sha(artifact_raw),
            "backupBytes": len(artifact_raw),
            "shadowReceiptSHA256": sha(shadow_raw),
            "incidentCandidateSHA": INCIDENT_CANDIDATE,
            "incidentBeforeDigest": INCIDENT_BEFORE,
            "beforeDigest": "1" * 64,
        }
        canonical = json.dumps(value, sort_keys=True, separators=(",", ":")).encode()
        value["signature"] = "hmac-sha256:" + hmac.new(KEY, b"norn.recovered-forward-evidence/v2\0" + canonical, hashlib.sha256).hexdigest()
        raw = strict_dump(value)
        self.evidence = self.private("source-evidence.json", raw)

    def verify(self, *extra):
        return subprocess.run(
            [str(SCRIPT), str(self.evidence), sha(self.evidence.read_bytes()), TRANSITION,
             SOURCE, CANDIDATE, str(self.proof), str(self.artifact), str(self.shadow), *extra],
            env=self.env, capture_output=True, text=True,
        )

    def test_exact_signed_evidence_and_local_artifacts_verify(self):
        result = self.verify()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), "verified")

    def test_v3_candidate_versioned_evidence_verifies_carried_forward_v2_baseline(self):
        prior_value = json.loads(self.evidence.read_text())
        prior_value["candidateSHA"] = "85fb2830ac18a711b2e3e20de45c1adcbbd4935d"
        prior_unsigned = {key: item for key, item in prior_value.items() if key != "signature"}
        prior_canonical = json.dumps(prior_unsigned, sort_keys=True, separators=(",", ":")).encode()
        prior_value["signature"] = "hmac-sha256:" + hmac.new(
            KEY, b"norn.recovered-forward-evidence/v2\0" + prior_canonical, hashlib.sha256).hexdigest()
        prior_raw = strict_dump(prior_value)
        prior_path = self.root / "transition.json.source-evidence.json"
        self.private(prior_path, prior_raw)
        versioned_dir = self.root / ("retarget-" + "e" * 40)
        versioned_dir.mkdir(mode=0o700)
        versioned_dir.chmod(0o700)
        new_candidate = "e" * 40
        artifact_raw = self.artifact.read_bytes()
        proof_raw = self.proof.read_bytes()
        shadow = json.loads(self.shadow.read_text())
        shadow["candidateSHA"] = new_candidate
        shadow_raw = strict_dump(shadow)
        self.shadow.write_bytes(shadow_raw)
        prior = json.loads(prior_raw)
        value = {key: item for key, item in prior.items() if key != "signature"}
        value.update({
            "schema": "norn.recovered-forward-evidence/v3",
            "candidateSHA": new_candidate,
            "preservationCapture": "carried-forward-verified",
            "carriedForwardEvidenceSHA256": sha(prior_raw),
            "carriedForwardEvidencePath": str(prior_path.resolve()),
            "backupProofSHA256": sha(proof_raw),
            "backupSHA256": sha(artifact_raw),
            "backupBytes": len(artifact_raw),
            "shadowReceiptSHA256": sha(shadow_raw),
        })
        canonical = json.dumps(value, sort_keys=True, separators=(",", ":")).encode()
        value["signature"] = "hmac-sha256:" + hmac.new(
            KEY, b"norn.recovered-forward-evidence/v3\0" + canonical, hashlib.sha256).hexdigest()
        self.evidence = self.private(versioned_dir / "source-evidence.json", strict_dump(value))
        result = subprocess.run(
            [str(SCRIPT), str(self.evidence), sha(self.evidence.read_bytes()), TRANSITION,
             SOURCE, new_candidate, str(self.proof), str(self.artifact), str(self.shadow)],
            env=self.env, capture_output=True, text=True,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), "verified")
        previous_path = self.evidence
        previous_raw = previous_path.read_bytes()
        next_candidate = "f" * 40
        next_dir = self.root / ("retarget-" + next_candidate)
        next_dir.mkdir(mode=0o700)
        next_dir.chmod(0o700)
        shadow["candidateSHA"] = next_candidate
        shadow_raw = strict_dump(shadow)
        self.shadow.write_bytes(shadow_raw)
        chained = {key: item for key, item in value.items() if key != "signature"}
        chained.update({
            "candidateSHA": next_candidate,
            "carriedForwardEvidenceSHA256": sha(previous_raw),
            "carriedForwardEvidencePath": str(previous_path.resolve()),
            "shadowReceiptSHA256": sha(shadow_raw),
        })
        canonical = json.dumps(chained, sort_keys=True, separators=(",", ":")).encode()
        chained["signature"] = "hmac-sha256:" + hmac.new(
            KEY, b"norn.recovered-forward-evidence/v3\0" + canonical, hashlib.sha256).hexdigest()
        self.evidence = self.private(next_dir / "source-evidence.json", strict_dump(chained))
        result = subprocess.run(
            [str(SCRIPT), str(self.evidence), sha(self.evidence.read_bytes()), TRANSITION,
             SOURCE, next_candidate, str(self.proof), str(self.artifact), str(self.shadow)],
            env=self.env, capture_output=True, text=True,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), "verified")
        chained["carriedForwardEvidenceSHA256"] = "0" * 64
        canonical = json.dumps({k: v for k, v in chained.items() if k != "signature"}, sort_keys=True, separators=(",", ":")).encode()
        chained["signature"] = "hmac-sha256:" + hmac.new(
            KEY, b"norn.recovered-forward-evidence/v3\0" + canonical, hashlib.sha256).hexdigest()
        self.evidence.write_bytes(strict_dump(chained))
        result = subprocess.run(
            [str(SCRIPT), str(self.evidence), sha(self.evidence.read_bytes()), TRANSITION,
             SOURCE, next_candidate, str(self.proof), str(self.artifact), str(self.shadow)],
            env=self.env, capture_output=True, text=True,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("prior evidence digest mismatch", result.stderr)

    def test_duplicate_keys_are_rejected_in_every_json_boundary(self):
        evidence_raw = self.evidence.read_text()
        self.evidence.write_text(evidence_raw.replace('"schema":', '"schema":"duplicate","schema":', 1))
        result = self.verify()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("duplicate JSON key", result.stderr)

        self.write_inputs(b'{"schema":"norn.legacy-control-backup/v1","schema":"norn.legacy-control-backup/v1"}')
        # Re-signing source evidence makes the raw duplicate proof digest a
        # valid binding; rejection must therefore come from strict parsing.
        result = self.verify()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("backup proof contains a duplicate JSON key", result.stderr)

    def test_changed_source_or_adoption_receipt_is_rejected(self):
        changed_source = subprocess.run(
            [str(SCRIPT), str(self.evidence), sha(self.evidence.read_bytes()), TRANSITION,
             "9" * 40, CANDIDATE, str(self.proof), str(self.artifact), str(self.shadow)],
            env=self.env, capture_output=True, text=True,
        )
        self.assertNotEqual(changed_source.returncode, 0)
        self.assertIn("exact transition and release SHAs", changed_source.stderr)

        self.adoption.write_bytes(self.adoption.read_bytes() + b" ")
        result = self.verify()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("adoption receipt digest mismatch", result.stderr)

    def test_noncanonical_evidence_fields_and_bool_size_are_rejected(self):
        value = json.loads(self.evidence.read_text())
        value["unexpected"] = True
        canonical = {k: v for k, v in value.items() if k != "signature"}
        signed = json.dumps(canonical, sort_keys=True, separators=(",", ":")).encode()
        value["signature"] = "hmac-sha256:" + hmac.new(KEY, b"norn.recovered-forward-evidence/v2\0" + signed, hashlib.sha256).hexdigest()
        self.evidence.write_bytes(strict_dump(value))
        result = self.verify()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("source evidence fields are not exact", result.stderr)


if __name__ == "__main__":
    unittest.main()
