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
TRANSITION = "fixture-transition-001"


def sha(raw: bytes) -> str:
    return hashlib.sha256(raw).hexdigest()


def strict_dump(value: dict) -> bytes:
    return (json.dumps(value, sort_keys=True, separators=(",", ":")) + "\n").encode()


class RecoveredForwardEvidenceTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.home = self.root / "home"
        (self.home / ".config/norn").mkdir(parents=True)
        self.env = os.environ.copy()
        self.env.update(HOME=str(self.home), NORN_AUDIT_SIGNING_KEY=KEY.decode())
        self.artifact = self.private("backup.dump", b"fixture database backup\n")
        self.proof = self.private("backup-proof.json", b"")
        self.shadow = self.private("shadow.json", b"")
        self.adoption_inputs = {}
        refs = {}
        for label in ("fence", "ledger", "reconciliation"):
            path = self.private(f"{label}.json", (label + " evidence\n").encode())
            refs[label] = {"path": str(path), "sha256": sha(path.read_bytes())}
        adoption = {
            "schema": "norn.recovered-baseline-adoption/v1",
            "candidateSHA": CANDIDATE,
            "adopterSHA": "a" * 40,
            "incidentBindingSHA256": "c" * 64,
            "preservationUnprovenReasonSHA256": "2" * 64,
            "legacyFence": refs["fence"],
            "ledger": refs["ledger"],
            "reconciliation": refs["reconciliation"],
            "preservationProven": False,
        }
        canonical = json.dumps(adoption, sort_keys=True, separators=(",", ":")).encode()
        adoption["signature"] = "hmac-sha256:" + hmac.new(KEY, b"norn.recovered-baseline-adoption/v1\0" + canonical, hashlib.sha256).hexdigest()
        self.adoption = self.private(self.home / ".config/norn/.norn-recovered-baseline-adoption.json", strict_dump(adoption))
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
            "schema": "norn.recovered-forward-evidence/v1",
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
            "beforeDigest": "1" * 64,
        }
        canonical = json.dumps(value, sort_keys=True, separators=(",", ":")).encode()
        value["signature"] = "hmac-sha256:" + hmac.new(KEY, b"norn.recovered-forward-evidence/v1\0" + canonical, hashlib.sha256).hexdigest()
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
        value["signature"] = "hmac-sha256:" + hmac.new(KEY, b"norn.recovered-forward-evidence/v1\0" + signed, hashlib.sha256).hexdigest()
        self.evidence.write_bytes(strict_dump(value))
        result = self.verify()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("fields or schema are not exact", result.stderr)


if __name__ == "__main__":
    unittest.main()
