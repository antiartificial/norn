#!/usr/bin/env python3
"""Exercise the artifact boundary used by the Mini private restore rehearsal."""

import datetime
import hashlib
import hmac
import json
import os
import pathlib
import subprocess
import tempfile
import unittest


VERIFY = pathlib.Path(__file__).with_name("mini-verify-protected-backup")
RELEASE = "a" * 40
URL = "postgresql://mini@127.0.0.1:5432/control"
KEY = "test-only-signing-key-longer-than-thirty-two-bytes"


class ProtectedBackupProofTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="norn-mini-backup-proof-")
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name)
        self.artifact = self.root / "control.dump"
        self.artifact.write_bytes(b"disposable custom archive fixture")
        self.artifact.chmod(0o600)
        self.proof = self.root / "proof.json"
        self.data = {
            "schema": "norn.legacy-control-backup/v1",
            "sourceReleaseSHA": RELEASE,
            "databaseIdentity": "hmac-sha256:" + hmac.new(
                KEY.encode(), b"norn.database-identity/v1\0" + URL.encode(), hashlib.sha256
            ).hexdigest(),
            "backupSHA256": hashlib.sha256(self.artifact.read_bytes()).hexdigest(),
            "backupBytes": self.artifact.stat().st_size,
            "createdAt": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        }
        self.write_proof()

    def write_proof(self):
        self.proof.write_text(json.dumps(self.data), encoding="utf-8")
        self.proof.chmod(0o600)

    def run_verify(self, release=RELEASE):
        return subprocess.run(
            [str(VERIFY), str(self.proof), str(self.artifact), release],
            env={**os.environ, "NORN_DATABASE_URL": URL, "NORN_AUDIT_SIGNING_KEY": KEY},
            capture_output=True,
            text=True,
            check=False,
        )

    def test_valid_proof_matches_artifact_and_source(self):
        result = self.run_verify()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), f"{self.data['backupSHA256']} {self.data['backupBytes']}")

    def test_rejects_wrong_release_identity_size_and_digest(self):
        self.assertNotEqual(self.run_verify("b" * 40).returncode, 0)
        for field, value in (
            ("databaseIdentity", "hmac-sha256:" + "0" * 64),
            ("backupBytes", self.data["backupBytes"] + 1),
            ("backupSHA256", "0" * 64),
        ):
            original = self.data[field]
            self.data[field] = value
            self.write_proof()
            self.assertNotEqual(self.run_verify().returncode, 0, field)
            self.data[field] = original
        self.write_proof()

    def test_rejects_stale_and_unsafe_files(self):
        self.data["createdAt"] = "2020-01-01T00:00:00Z"
        self.write_proof()
        self.assertNotEqual(self.run_verify().returncode, 0)
        self.data["createdAt"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        self.write_proof()
        self.artifact.chmod(0o644)
        self.assertNotEqual(self.run_verify().returncode, 0)
        self.artifact.chmod(0o600)
        target = self.root / "real-proof.json"
        self.proof.rename(target)
        self.proof.symlink_to(target)
        self.assertNotEqual(self.run_verify().returncode, 0)


if __name__ == "__main__":
    unittest.main()
