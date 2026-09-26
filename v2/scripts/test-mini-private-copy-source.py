#!/usr/bin/env python3
"""Check Mini source URL fidelity before any read-only dump is opened."""

import os
from pathlib import Path
import subprocess
import tempfile
import unittest
import urllib.parse


SCRIPT = Path(__file__).with_name("mini-private-copy-rehearsal")
SHA = "a" * 40
CONTRACT = (
    '{"name":"norn.startup/v2","schemaModes":["migrate-only"],'
    '"startupModes":["passive"],"schemaContract":{'
    '"catalogMigrationVersion":1,"catalogMinimumReaderVersion":1,'
    '"catalogMinimumWriterVersion":1}}'
)


class MiniPrivateCopySourceTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.postgres_bin = self.root / "postgres"
        self.postgres_bin.mkdir()
        self.capture = self.root / "source-env.txt"
        for name in ("initdb", "pg_ctl", "createdb", "pg_restore", "psql"):
            path = self.postgres_bin / name
            path.write_text("#!/bin/sh\nexit 99\n")
            path.chmod(0o700)
        dump = self.postgres_bin / "pg_dump"
        dump.write_text(
            "#!/bin/sh\n"
            "printf '%s\\n' \"$PGHOST\" \"${PGHOSTADDR:-}\" \"$PGSSLMODE\" "
            "\"${PGSSLROOTCERT:-}\" \"$PGOPTIONS\" \"${PGSERVICE:-}\" > \"$CAPTURE_FILE\"\n"
            "exit 23\n"
        )
        dump.chmod(0o700)
        self.candidate = self.root / "candidate"
        self.candidate.write_text(f"#!/bin/sh\nprintf '%s\\n' '{CONTRACT}'\n")
        self.candidate.chmod(0o700)

    def run_source(self, query):
        env = os.environ.copy()
        env.update({
            "NORN_CANDIDATE_SHA": SHA,
            "NORN_REHEARSAL_POSTGRES_BIN": str(self.postgres_bin),
            "NORN_REHEARSAL_SOURCE_DATABASE_URL": "postgresql://norn@/norn_private?" + query,
            "CAPTURE_FILE": str(self.capture),
            "PGHOSTADDR": "192.0.2.99",
            "PGSERVICE": "unrelated-inherited-service",
            "PYTHONOPTIMIZE": "1",
        })
        return subprocess.run([str(SCRIPT), str(self.candidate)], env=env, capture_output=True, text=True, timeout=15)

    def test_supported_parameters_reach_dump_without_inherited_overrides(self):
        query = urllib.parse.urlencode({
            "host": str(self.root), "hostaddr": "127.0.0.1", "sslmode": "verify-full",
            "sslrootcert": str(self.root / "server-ca.pem"),
        })
        result = self.run_source(query)
        self.assertEqual(result.returncode, 23, result.stderr)
        self.assertEqual(self.capture.read_text().splitlines(), [
            str(self.root), "127.0.0.1", "verify-full",
            str(self.root / "server-ca.pem"), "-c default_transaction_read_only=on", "",
        ])

    def test_unknown_or_repeated_parameter_fails_before_dump(self):
        host = urllib.parse.urlencode({"host": str(self.root)})
        for query in (host + "&options=-c%20default_transaction_read_only%3Doff",
                      host + "&sslmode=verify-full&sslmode=disable"):
            with self.subTest(query=query):
                result = self.run_source(query)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(self.capture.exists(), result.stderr)


if __name__ == "__main__":
    unittest.main()
