#!/usr/bin/env python3
"""Check Mini source URL fidelity before any read-only dump is opened."""

import os
import signal
from pathlib import Path
import subprocess
import tempfile
import time
import unittest
import urllib.parse


SCRIPT = Path(__file__).with_name("mini-private-copy-rehearsal")
SHA = "a" * 40
VERSION = "v2.20.0-platform-40-gaaaaaaaa"
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

    def run_source(self, query, extra_environment=None):
        env = os.environ.copy()
        env.update({
            "NORN_CANDIDATE_SHA": SHA,
            "NORN_CANDIDATE_VERSION": VERSION,
            "NORN_REHEARSAL_POSTGRES_BIN": str(self.postgres_bin),
            "NORN_REHEARSAL_SOURCE_DATABASE_URL": "postgresql://norn@/norn_private?" + query,
            "CAPTURE_FILE": str(self.capture),
            "PGHOSTADDR": "192.0.2.99",
            "PGSERVICE": "unrelated-inherited-service",
            "PYTHONOPTIMIZE": "1",
        })
        if extra_environment:
            env.update(extra_environment)
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

    def test_candidate_identity_requires_a_signed_release_label_bound_to_its_sha(self):
        query = urllib.parse.urlencode({"host": str(self.root)})
        for environment in (
            {"NORN_CANDIDATE_VERSION": ""},
            {"NORN_CANDIDATE_VERSION": "v2.20.0-platform-40-gbbbbbbbb"},
            {"NORN_CANDIDATE_VERSION": SHA},
        ):
            with self.subTest(environment=environment):
                result = self.run_source(query, environment)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(self.capture.exists(), result.stderr)

    def test_term_after_private_scratch_setup_preserves_nonzero_signal_status(self):
        dump = self.postgres_bin / "pg_dump"
        dump.write_text("#!/bin/sh\nsleep 30\n")
        dump.chmod(0o700)
        env = os.environ.copy()
        env.update({
            "NORN_CANDIDATE_SHA": SHA, "NORN_CANDIDATE_VERSION": VERSION,
            "NORN_REHEARSAL_POSTGRES_BIN": str(self.postgres_bin),
            "NORN_REHEARSAL_SOURCE_DATABASE_URL": "postgresql://norn@/norn_private?" + urllib.parse.urlencode({"host": str(self.root)}),
        })
        process = subprocess.Popen([str(SCRIPT), str(self.candidate)], env=env, stdout=subprocess.PIPE,
                                   stderr=subprocess.PIPE, text=True, start_new_session=True)
        time.sleep(0.2)
        os.killpg(process.pid, signal.SIGTERM)
        _, stderr = process.communicate(timeout=10)
        self.assertEqual(process.returncode, 143, stderr)


if __name__ == "__main__":
    unittest.main()
