#!/usr/bin/env python3
"""Exercise the recovered-forward journal writer's first receipt binding."""

import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("platform-upgrade")
MARKER = 'python3 - "$transition_state_path" "$legacy_release_sha" "$pid" "$state" <<\'PY\''
CODE = SCRIPT.read_text().split(MARKER, 1)[1].split("\n", 1)[1].split("\nPY\n}", 1)[0]


class JournalBindingTests(unittest.TestCase):
    def test_first_preservation_digest_is_bound_once(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            receipt = root / "preservation-receipt.json"
            receipt.write_text('{"signed":"fixture"}')
            receipt.chmod(0o600)
            digest = hashlib.sha256(receipt.read_bytes()).hexdigest()
            journal = root / "transition.json"
            journal.write_text(json.dumps({
                "schema": "norn.recovered-forward-transition/v1",
                "legacyReleaseSHA": "a" * 40,
                "state": "candidate-preservation-pending",
                "preservationReceiptPath": str(receipt),
                "preservationReceiptSHA256": "",
            }))
            journal.chmod(0o600)
            env = os.environ.copy()
            env.update({
                "NORN_RECOVERED_FORWARD_JOURNAL_SCHEMA": "norn.recovered-forward-transition/v1",
                "NORN_RECOVERED_FORWARD_PRESERVATION_RECEIPT": str(receipt),
                "NORN_RECOVERED_FORWARD_PRESERVATION_RECEIPT_SHA256": digest,
            })
            command = [sys.executable, "-c", CODE, str(journal), "a" * 40, "0", "candidate-finalize-requested"]
            subprocess.run(command, env=env, check=True, capture_output=True)
            self.assertEqual(json.loads(journal.read_text())["preservationReceiptSHA256"], digest)
            env["NORN_RECOVERED_FORWARD_PRESERVATION_RECEIPT_SHA256"] = "0" * 64
            failed = subprocess.run(command, env=env, capture_output=True, text=True)
            self.assertNotEqual(failed.returncode, 0)
            self.assertIn("binding changed", failed.stderr)


if __name__ == "__main__":
    unittest.main()
