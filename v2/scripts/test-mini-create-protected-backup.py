#!/usr/bin/env python3
"""Exercise protected backup file, proof, environment, and failure boundaries."""

import json
import os
from pathlib import Path
import stat
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).parent
CREATE = ROOT / "mini-create-protected-backup"
VERIFY = ROOT / "mini-verify-protected-backup"
RELEASE = "a" * 40
KEY = "test-only-signing-key-longer-than-thirty-two-bytes"


class ProtectedBackupProducerTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="norn-mini-backup-producer-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.output = self.root / "out"
        self.output.mkdir(mode=0o700)
        self.socket = self.root / "socket"
        self.socket.mkdir()
        self.tools = self.root / "tools"
        self.tools.mkdir()
        self.url = f"postgresql://fixture@localhost:5432/control?host={self.socket}&sslmode=disable"
        self.env = {**os.environ, "NORN_DATABASE_URL": self.url, "NORN_AUDIT_SIGNING_KEY": KEY}

    def tool(self, name, body):
        path = self.tools / name
        path.write_text("#!/usr/bin/env python3\n" + body, encoding="utf-8")
        path.chmod(0o755)

    def create(self):
        return subprocess.run(
            [str(CREATE), "--legacy-release", RELEASE, "--output-dir", str(self.output), "--postgres-bin", str(self.tools)],
            env=self.env, text=True, capture_output=True, check=False,
        )

    def test_verifies_owner_only_artifact_and_scrubs_dump_environment(self):
        self.tool("pg_dump", """import json, os, sys
with open(os.environ['BACKUP_TEST_OBSERVATION'], 'w') as out:
    json.dump({'host':os.environ.get('PGHOST'), 'options':os.environ.get('PGOPTIONS'),
               'url_present':'NORN_DATABASE_URL' in os.environ,
               'key_present':'NORN_AUDIT_SIGNING_KEY' in os.environ}, out)
sys.stdout.buffer.write(b'synthetic custom archive')
""")
        self.tool("pg_restore", "import sys\nassert sys.argv[1] == '--list'\n")
        observation = self.root / "observation.json"
        self.env["BACKUP_TEST_OBSERVATION"] = str(observation)
        result = self.create()
        self.assertEqual(result.returncode, 0, result.stderr)
        artifact, = self.output.glob("*.dump")
        proof, = self.output.glob("*.proof.json")
        self.assertEqual(stat.S_IMODE(artifact.stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(proof.stat().st_mode), 0o600)
        observed = json.loads(observation.read_text())
        self.assertEqual(observed["host"], str(self.socket))
        self.assertEqual(observed["options"], "-c default_transaction_read_only=on")
        self.assertFalse(observed["url_present"] or observed["key_present"])
        verified = subprocess.run([str(VERIFY), str(proof), str(artifact), RELEASE], env=self.env, text=True, capture_output=True, check=False)
        self.assertEqual(verified.returncode, 0, verified.stderr)

    def test_failed_dump_removes_partial_files(self):
        self.tool("pg_dump", "import sys\nsys.stdout.buffer.write(b'partial')\nsys.exit(7)\n")
        result = self.create()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(list(self.output.iterdir()), [])
        self.assertNotIn(KEY, result.stderr)


if __name__ == "__main__":
    unittest.main()
