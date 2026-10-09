#!/usr/bin/env python3
"""Check Mini source URL fidelity before any read-only dump is opened."""

import json
import os
import signal
from pathlib import Path
import subprocess
import tempfile
import textwrap
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

    def install_table_fixture(self):
        """Let the rehearsal reach its restored-schema table validation."""
        for name in ("initdb", "pg_ctl", "createdb", "pg_restore"):
            path = self.postgres_bin / name
            path.write_text("#!/bin/sh\nexit 0\n")
            path.chmod(0o700)
        dump = self.postgres_bin / "pg_dump"
        dump.write_text(
            "#!/bin/sh\n"
            "for argument in \"$@\"; do\n"
            "  case \"$argument\" in --file=*) : > \"${argument#--file=}\";; esac\n"
            "done\n"
        )
        dump.chmod(0o700)
        psql = self.postgres_bin / "psql"
        psql.write_text(
            "#!/bin/sh\n"
            "query=\n"
            "while [ \"$#\" -gt 0 ]; do\n"
            "  if [ \"$1\" = -c ]; then query=$2; shift 2; else shift; fi\n"
            "done\n"
            "if [ -n \"${NORN_TEST_PSQL_LOG:-}\" ]; then printf '%s\\n' \"$query\" >> \"$NORN_TEST_PSQL_LOG\"; fi\n"
            "case \"$query\" in\n"
            "  *\"SELECT tablename FROM pg_tables\"*)\n"
            "    [ -z \"${NORN_TEST_TABLES:-}\" ] || printf '%s\\n' \"$NORN_TEST_TABLES\";;\n"
            "  *\"FROM pg_index\"*)\n"
            "    case \"$query\" in *\"table_58\"*) exit 0;; *) printf 'id\\n';; esac;;\n"
            "  *\"FROM pg_attribute\"*) printf 'id\\n';;\n"
            "  *\"SELECT count(*) FROM public.\"*) printf '0\\n';;\n"
            "  *\"COPY (SELECT row_to_json\"*) printf '{}\\n';;\n"
            "esac\n"
        )
        psql.chmod(0o700)

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

    def test_more_than_28_tables_reaches_the_last_table_primary_key_gate(self):
        self.install_table_fixture()
        tables = [f"table_{number:02d}" for number in range(59)]
        psql_log = self.root / "psql.log"
        result = self.run_source(urllib.parse.urlencode({"host": str(self.root)}), {
            "NORN_TEST_TABLES": "\n".join(tables),
            "NORN_TEST_PSQL_LOG": str(psql_log),
        })
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("original table has no primary key: table_58", result.stderr)
        self.assertNotIn("expected 28 original public tables", result.stderr)
        self.assertIn('COPY (SELECT row_to_json(full_row)::text FROM (SELECT id FROM public."table_57"',
                      psql_log.read_text())

    def test_no_original_tables_is_rejected_before_fingerprinting(self):
        self.install_table_fixture()
        result = self.run_source(urllib.parse.urlencode({"host": str(self.root)}))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("expected at least one original public table, found none", result.stderr)

    def test_migration_metadata_is_validated_separately_from_preserved_data(self):
        for name in ("initdb", "pg_ctl", "createdb", "pg_restore"):
            path = self.postgres_bin / name
            path.write_text("#!/bin/sh\nexit 0\n")
            path.chmod(0o700)
        dump = self.postgres_bin / "pg_dump"
        dump.write_text(textwrap.dedent('''\
            #!/bin/sh
            for argument in "$@"; do
              case "$argument" in --file=*) : > "${argument#--file=}";; esac
            done
        '''))
        dump.chmod(0o700)
        (self.postgres_bin / "postgres").write_text("#!/bin/sh\necho PostgreSQL-test\n")
        (self.postgres_bin / "postgres").chmod(0o700)

        psql_log = self.root / "fingerprint-queries.txt"
        psql = self.postgres_bin / "psql"
        psql.write_text(textwrap.dedent('''\
            #!/bin/sh
            query=
            while [ "$#" -gt 0 ]; do
              if [ "$1" = -c ]; then query=$2; shift 2; else shift; fi
            done
            [ -z "${NORN_TEST_PSQL_LOG:-}" ] || printf '%s\n' "$query" >> "$NORN_TEST_PSQL_LOG"
            case "$query" in
              *"SELECT tablename FROM pg_tables"*) printf 'app_table\nnorn_schema_compatibility\nnorn_schema_migrations\n';;
              *"SELECT count(*) FROM public."*) printf '1\n';;
              *"FROM pg_index"*|*"FROM pg_attribute"*) printf 'id\n';;
              *"COPY (SELECT row_to_json"*) printf '{"id":1}\n';;
              *"bool_and(length(checksum)=64)"*) printf '49|1|49|t\n';;
              *"FROM norn_schema_compatibility WHERE singleton"*) printf '1|49|5|32\n';;
            esac
        '''))
        psql.chmod(0o700)

        tools = self.root / "bin"
        tools.mkdir()
        curl = tools / "curl"
        curl.write_text(textwrap.dedent('''\
            #!/bin/sh
            url=
            for argument in "$@"; do case "$argument" in http*) url=$argument;; esac; done
            case "$url" in
              */api/health) printf '{"status":"passive"}\n';;
              */api/version) printf '{"version":"v2.20.0-platform-40-gaaaaaaaa"}\n';;
              */api/schema) printf '{"version":"v2.20.0-platform-40-gaaaaaaaa","startupContract":"norn.startup/v2","startupMode":"passive","schemaMode":"check","currentMigrationVersion":49,"minimumReaderVersion":5,"minimumWriterVersion":32,"operationRecoveryEnabled":false,"operationWorkerEnabled":false,"nomadWatcherEnabled":false}\n';;
              *) exit 1;;
            esac
        '''))
        curl.chmod(0o700)

        candidate_contract = json.loads(CONTRACT)
        candidate_contract["schemaContract"] = {
            "catalogMigrationVersion": 49,
            "catalogMinimumReaderVersion": 5,
            "catalogMinimumWriterVersion": 32,
        }
        self.candidate.write_text(textwrap.dedent(f'''\
            #!/usr/bin/env python3
            import json, os, signal, sys, time
            contract={json.dumps(candidate_contract)!r}
            def raise_exit(*_): raise SystemExit(0)
            if sys.argv[1:] == ['--norn-startup-contract']: print(contract); raise SystemExit(0)
            if os.environ.get('NORN_SCHEMA_MODE') == 'migrate-only': raise SystemExit(0)
            signal.signal(signal.SIGTERM, raise_exit)
            while True: time.sleep(1)
        '''))
        self.candidate.chmod(0o700)
        env = {
            **os.environ,
            "NORN_CANDIDATE_SHA": SHA,
            "NORN_CANDIDATE_VERSION": VERSION,
            "NORN_REHEARSAL_POSTGRES_BIN": str(self.postgres_bin),
            "NORN_REHEARSAL_SOURCE_DATABASE_URL": "postgresql://norn@/norn_private?" + urllib.parse.urlencode({"host": str(self.root)}),
            "NORN_TEST_PSQL_LOG": str(psql_log),
            "PATH": str(tools) + os.pathsep + os.environ.get("PATH", ""),
        }
        result = subprocess.run([str(SCRIPT), str(self.candidate)], env=env, capture_output=True, text=True, timeout=20)
        self.assertEqual(result.returncode, 0, result.stderr)
        queries = psql_log.read_text()
        self.assertIn('COPY (SELECT row_to_json(full_row)::text FROM (SELECT id FROM public."app_table"', queries)
        self.assertNotIn('FROM public."norn_schema_migrations"', queries)
        self.assertNotIn('FROM public."norn_schema_compatibility"', queries)
        self.assertIn("migration_metadata_tables=norn_schema_migrations,norn_schema_compatibility-validated", result.stdout)
        self.assertIn("fingerprinted_tables=1", result.stdout)

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
