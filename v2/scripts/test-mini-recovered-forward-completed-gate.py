#!/usr/bin/env python3
"""Contract fixtures for the finalized recovered-forward upgrade gate."""

import hashlib
import hmac
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("mini-recovered-forward-completed-gate")
SOURCE = "1" * 40
CANDIDATE = "2" * 40
AUDIT_KEY = "fixture-audit-signing-key-32-characters"


def write(path: Path, data: bytes | str, mode: int = 0o600) -> Path:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(data.encode() if isinstance(data, str) else data)
    path.chmod(mode)
    return path


class CompletedRecoveryGateTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.home = self.root / "home"
        self.home.mkdir()
        self.releases = self.root / "releases"
        for sha in (SOURCE, CANDIDATE):
            folder = self.releases / sha
            folder.mkdir(parents=True)
            write(folder / "release.env", f"NORN_RELEASE_SHA={sha}\n", 0o644)
        self.current = self.root / "current"
        self.current.symlink_to(self.releases / CANDIDATE)
        self.journal = self.home / ".config/norn/recovered-forward/transition.json"
        self.preservation = self.home / ".config/norn/recovered-forward/preservation-receipt.json"
        self.proof = write(self.root / "backup.proof.json", b"proof")
        self.backup = write(self.root / "backup.dump", b"backup")
        self.shadow = write(self.root / "shadow.json", b"shadow")
        self.evidence = write(self.home / ".config/norn/recovered-forward/transition.json.source-evidence.json", b"evidence")
        receipt = {
            "schema": "norn.recovered-forward-preservation/v1",
            "transitionId": "mini-v3-recovery-test",
            "candidateSHA": CANDIDATE,
            "sourceEvidenceSHA256": hashlib.sha256(self.evidence.read_bytes()).hexdigest(),
            "beforeDigest": "3" * 64,
            "afterDigest": "3" * 64,
            "preservationMatched": True,
            "finalizedAt": "2026-10-12T00:00:00Z",
        }
        canonical = json.dumps(receipt, sort_keys=True, separators=(",", ":")).encode()
        receipt["signature"] = "hmac-sha256:" + hmac.new(
            AUDIT_KEY.encode(), receipt["schema"].encode() + b"\0" + canonical, hashlib.sha256).hexdigest()
        raw_preservation = (json.dumps(receipt, sort_keys=True, separators=(",", ":")) + "\n").encode()
        write(self.preservation, raw_preservation)
        self.value = {
            "schema": "norn.recovered-forward-transition/v1",
            "transitionID": "mini-v3-recovery-test",
            "sourceSHA": SOURCE,
            "candidateReleaseSHA": CANDIDATE,
            "sourceEvidencePath": str(self.evidence),
            "sourceEvidenceSHA256": hashlib.sha256(self.evidence.read_bytes()).hexdigest(),
            "backupProofPath": str(self.proof),
            "backupArtifactPath": str(self.backup),
            "shadowReceiptPath": str(self.shadow),
            "preservationReceiptPath": str(self.preservation),
            "preservationReceiptSHA256": hashlib.sha256(raw_preservation).hexdigest(),
            "state": "candidate-promoted",
        }
        write(self.journal, json.dumps(self.value, sort_keys=True) + "\n")

    def invoke(self):
        return subprocess.run([str(SCRIPT), str(self.journal), str(self.current), str(self.releases), str(self.preservation)],
                              text=True, capture_output=True,
                              env={**os.environ, "HOME": str(self.home), "NORN_AUDIT_SIGNING_KEY": AUDIT_KEY}, timeout=10)

    def test_exact_promoted_candidate_and_preservation_receipt_pass(self):
        result = self.invoke()
        self.assertEqual(result.returncode, 0, result.stderr)
        fields = result.stdout.rstrip("\n").split("\t")
        self.assertEqual(fields[:3], ["mini-v3-recovery-test", SOURCE, CANDIDATE])
        self.assertEqual(fields[-1], self.value["preservationReceiptSHA256"])

    def test_incomplete_or_failed_transition_cannot_authorize(self):
        for phase in ("candidate-postflight-failed", "candidate-normal-active-pending-host-agent", "candidate-promoted-typo"):
            with self.subTest(phase=phase):
                self.value["state"] = phase
                write(self.journal, json.dumps(self.value) + "\n")
                result = self.invoke()
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("fully promoted", result.stderr)
        self.value["state"] = "candidate-promoted"

    def test_current_release_must_match_journaled_candidate(self):
        self.current.unlink()
        self.current.symlink_to(self.releases / SOURCE)
        result = self.invoke()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("exact promoted recovery candidate", result.stderr)

    def test_preservation_receipt_tampering_fails(self):
        self.preservation.write_text("tampered\n")
        result = self.invoke()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("digest changed", result.stderr)

    def test_rehashed_but_unsigned_preservation_receipt_fails(self):
        receipt = json.loads(self.preservation.read_text())
        receipt["afterDigest"] = "4" * 64
        raw = (json.dumps(receipt, sort_keys=True, separators=(",", ":")) + "\n").encode()
        write(self.preservation, raw)
        self.value["preservationReceiptSHA256"] = hashlib.sha256(raw).hexdigest()
        write(self.journal, json.dumps(self.value, sort_keys=True) + "\n")
        result = self.invoke()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("signature does not verify", result.stderr)

    def test_journal_mode_and_release_identity_are_checked(self):
        self.journal.chmod(0o644)
        result = self.invoke()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("mode-0600", result.stderr)
        self.journal.chmod(0o600)
        write(self.releases / CANDIDATE / "release.env", f"NORN_RELEASE_SHA={SOURCE}\n", 0o644)
        result = self.invoke()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("does not match its directory", result.stderr)


if __name__ == "__main__":
    unittest.main()
