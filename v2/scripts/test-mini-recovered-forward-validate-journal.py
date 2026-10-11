#!/usr/bin/env python3
"""Fixture tests for exact recovered-forward journal phase resumption."""

import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("mini-recovered-forward-validate-journal")
PHASES = (
    "prepared", "host-agent-fencing", "host-agent-fenced", "api-fencing",
    "legacy-fenced", "migration-started", "schema-migrated",
    "candidate-activation-started", "candidate-postflight-failed",
    "candidate-preservation-pending", "candidate-finalize-requested",
    "candidate-normal-activation-pending", "candidate-normal-active-pending-host-agent",
    "candidate-normal-active-pending-host-agent-restart", "candidate-promoted",
)


class RecoveredForwardJournalTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.journal = self.root / "transition.json"
        self.record = {
            "schema": "norn.recovered-forward-transition/v1",
            "transitionID": "fixture-001",
            "sourceSHA": "a" * 40,
            "candidateReleaseSHA": "b" * 40,
            "sourceEvidencePath": "/tmp/transition.json.source-evidence.json",
            "sourceEvidenceSHA256": "c" * 64,
            "backupProofPath": "/tmp/backup-proof.json",
            "backupArtifactPath": "/tmp/backup.dump",
            "shadowReceiptPath": "/tmp/shadow.json",
            "state": "prepared",
        }

    def write(self, value=None, mode=0o600):
        self.journal.write_text(json.dumps(value or self.record), encoding="utf-8")
        self.journal.chmod(mode)

    def invoke(self):
        return subprocess.run(
            [str(SCRIPT), str(self.journal), self.record["transitionID"], self.record["sourceSHA"],
             self.record["candidateReleaseSHA"], self.record["sourceEvidencePath"],
             self.record["sourceEvidenceSHA256"], self.record["backupProofPath"],
             self.record["backupArtifactPath"], self.record["shadowReceiptPath"]],
            capture_output=True, text=True, env=os.environ.copy(),
        )

    def test_every_durable_interruption_phase_is_recognized_with_exact_bindings(self):
        for phase in PHASES:
            with self.subTest(phase=phase):
                value = {**self.record, "state": phase}
                self.write(value)
                result = self.invoke()
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout.strip(), phase)

    def test_altered_identity_paths_and_unsupported_phase_refuse_resume(self):
        cases = [
            ("transitionID", "other-transition"),
            ("sourceSHA", "d" * 40),
            ("candidateReleaseSHA", "e" * 40),
            ("sourceEvidenceSHA256", "f" * 64),
            ("sourceEvidencePath", "/tmp/rebound.json"),
            ("backupProofPath", "/tmp/rebound-proof.json"),
            ("backupArtifactPath", "/tmp/rebound-backup.dump"),
            ("shadowReceiptPath", "/tmp/rebound-shadow.json"),
            ("state", "legacy-baseline-replay"),
        ]
        for key, changed in cases:
            with self.subTest(key=key):
                value = {**self.record, key: changed}
                self.write(value)
                result = self.invoke()
                self.assertNotEqual(result.returncode, 0)

    def test_duplicate_keys_and_unsafe_journal_mode_refuse_resume(self):
        self.journal.write_text('{"schema":"norn.recovered-forward-transition/v1","schema":"norn.recovered-forward-transition/v1"}')
        self.journal.chmod(0o600)
        self.assertNotEqual(self.invoke().returncode, 0)
        self.write(mode=0o644)
        result = self.invoke()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("mode-0600", result.stderr)


if __name__ == "__main__":
    unittest.main()
