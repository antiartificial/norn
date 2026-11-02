#!/usr/bin/env python3
import hashlib
import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest


script = Path(__file__).with_name("mini-historical-shadow-evidence")
loader = importlib.machinery.SourceFileLoader("historical", str(script))
spec = importlib.util.spec_from_loader(loader.name, loader)
historical = importlib.util.module_from_spec(spec)
loader.exec_module(historical)


class HistoricalShadowEvidenceTest(unittest.TestCase):
    source = "3" * 40
    candidate = "4" * 40

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        root = Path(self.temp.name)
        self.artifact = root / "control.dump"
        self.proof = root / "proof.json"
        self.receipt = root / "receipt.json"
        self.write(self.artifact, b"old-copy")
        self.write(self.proof, json.dumps({
            "schema": "norn.legacy-control-backup/v1",
            "sourceReleaseSHA": self.source,
            "databaseIdentity": "hmac-sha256:" + "a" * 64,
            "backupSHA256": hashlib.sha256(b"old-copy").hexdigest(),
            "backupBytes": 8,
            "createdAt": "2025-01-01T00:00:00Z",
        }).encode())
        self.write(self.receipt, json.dumps({
            "schema": "norn.m5-signed-shadow-launchagent-rehearsal/v2",
            "candidateSHA": self.candidate,
            "backupSHA256": hashlib.sha256(b"old-copy").hexdigest(),
            "backupBytes": 8,
            "completedAt": "2025-01-01T00:01:00Z",
            "exitCode": 0,
            "privateRestoreClaim": "passed",
            "runtimeClaim": "passed",
        }).encode())

    def write(self, path, data):
        path.write_bytes(data)
        os.chmod(path, 0o600)

    def inspect(self):
        return historical.inspect(str(self.proof), str(self.artifact), str(self.receipt), self.source, self.candidate)

    def test_accepts_historical_pair_only_as_evidence(self):
        result = self.inspect()
        self.assertEqual(result["status"], "historical-shadow-evidence-only")
        self.assertEqual(result["backupBytes"], 8)

    def test_rejects_modified_artifact(self):
        self.write(self.artifact, b"old-copY")
        with self.assertRaisesRegex(SystemExit, "digest differs"):
            self.inspect()

    def test_rejects_unbound_receipt(self):
        data = json.loads(self.receipt.read_text())
        data["candidateSHA"] = "5" * 40
        self.write(self.receipt, json.dumps(data).encode())
        with self.assertRaisesRegex(SystemExit, "not bound"):
            self.inspect()

    def test_rejects_symlink(self):
        self.artifact.unlink()
        self.artifact.symlink_to(self.proof)
        with self.assertRaises(OSError):
            self.inspect()

    def test_rejects_broad_mode(self):
        os.chmod(self.proof, 0o644)
        with self.assertRaisesRegex(SystemExit, "mode-0600"):
            self.inspect()


if __name__ == "__main__":
    unittest.main()
