#!/usr/bin/env python3
"""Contract tests for preparing a guarded recovered-forward retarget bundle."""

import hashlib
import hmac
import importlib.machinery
import importlib.util
import json
import os
import stat
import sys
import tempfile
import types
import unittest
from pathlib import Path
from unittest.mock import patch

SCRIPT = Path(__file__).with_name("mini-recovered-forward-retarget")
LOADER = importlib.machinery.SourceFileLoader("mini_recovered_forward_retarget", str(SCRIPT))
SPEC = importlib.util.spec_from_loader(LOADER.name, LOADER)
RETARGET = importlib.util.module_from_spec(SPEC)
assert SPEC and SPEC.loader
SPEC.loader.exec_module(RETARGET)
CONSUMER_PATH = Path(__file__).with_name("mini-recovered-forward-retarget-consume")
CONSUMER_LOADER = importlib.machinery.SourceFileLoader("mini_recovered_forward_retarget_consume", str(CONSUMER_PATH))
CONSUMER_SPEC = importlib.util.spec_from_loader(CONSUMER_LOADER.name, CONSUMER_LOADER)
CONSUMER = importlib.util.module_from_spec(CONSUMER_SPEC)
assert CONSUMER_SPEC and CONSUMER_SPEC.loader
CONSUMER_SPEC.loader.exec_module(CONSUMER)


class RetargetTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.key = b"test-only-retarget-signing-key-32bytes"
        self.source = "a" * 40
        self.old_candidate = "b" * 40
        self.new_candidate = "c" * 40
        self.transition = "mini-v3-retarget-test"
        self.identity = "hmac-sha256:" + hmac.new(
            self.key, b"norn.database-identity/v1\0postgres://fixture/norn", hashlib.sha256).hexdigest()
        self.evidence_path = self.root / "transition.json.source-evidence.json"
        self.journal_path = self.root / "transition.json"
        self.old_evidence = {
            "schema": RETARGET.OLD_EVIDENCE_SCHEMA,
            "transitionId": self.transition,
            "sourceSHA": self.source,
            "candidateSHA": self.old_candidate,
            "databaseIdentity": self.identity,
            "recoveryFenceSHA256": "1" * 64,
            "adoptionReceiptSHA256": "2" * 64,
            "incidentBindingSHA256": "3" * 64,
            "ledgerSHA256": "4" * 64,
            "reconciliationSHA256": "5" * 64,
            "backupProofSHA256": "6" * 64,
            "backupSHA256": "7" * 64,
            "backupBytes": 321,
            "shadowReceiptSHA256": "8" * 64,
            "incidentCandidateSHA": "9" * 40,
            "incidentBeforeDigest": "a" * 64,
            "beforeDigest": "b" * 64,
        }
        self.evidence_raw = RETARGET.sign_document(self.old_evidence, self.key)
        self.write_private(self.evidence_path, self.evidence_raw)
        self.journal = {
            "schema": RETARGET.JOURNAL_SCHEMA,
            "transitionID": self.transition,
            "sourceSHA": self.source,
            "candidateReleaseSHA": self.old_candidate,
            "sourceEvidencePath": str(self.evidence_path),
            "sourceEvidenceSHA256": RETARGET.digest(self.evidence_raw),
            "backupProofPath": str(self.root / "old-proof.json"),
            "backupArtifactPath": str(self.root / "old-backup.dump"),
            "shadowReceiptPath": str(self.root / "old-shadow.json"),
            "state": "legacy-fenced",
            "legacyReleaseSHA": self.source,
            "legacyPID": 0,
        }
        self.journal_raw = RETARGET.canonical(self.journal) + b"\n"
        self.write_private(self.journal_path, self.journal_raw)

    def write_private(self, path, raw):
        path.write_bytes(raw)
        path.chmod(0o600)

    def test_only_exact_source_bound_legacy_fenced_candidate_is_accepted(self):
        args = type("Args", (), {"journal": self.journal_path, "source_sha": self.source,
                                 "candidate_sha": self.new_candidate, "transition_id": self.transition,
                                 "evidence": self.evidence_path})
        journal, evidence, path = RETARGET.verify_source_binding(args, self.key)
        self.assertEqual(journal["state"], "legacy-fenced")
        self.assertEqual(evidence["candidateSHA"], self.old_candidate)
        self.assertEqual(path, self.evidence_path)

        for field, value in (("state", "migration-started"),
                             ("candidateReleaseSHA", "d" * 40),
                             ("sourceSHA", "e" * 40),
                             ("transitionID", "another-transition")):
            with self.subTest(field=field):
                changed = dict(self.journal, **{field: value})
                self.write_private(self.journal_path, RETARGET.canonical(changed) + b"\n")
                with self.assertRaises(SystemExit):
                    RETARGET.verify_source_binding(args, self.key)
        self.write_private(self.journal_path, self.journal_raw)

    def test_accepts_signed_candidate_versioned_v3_evidence_for_retarget_chain(self):
        previous = "d" * 40
        evidence_dir = self.root / f"retarget-{previous}"
        evidence_dir.mkdir(mode=0o700)
        evidence_dir.chmod(0o700)
        evidence_path = evidence_dir / "source-evidence.json"
        carried = {
            **{key: value for key, value in self.old_evidence.items() if key not in {"schema", "candidateSHA", "backupProofSHA256", "backupSHA256", "backupBytes", "shadowReceiptSHA256", "signature"}},
            "schema": RETARGET.EVIDENCE_SCHEMA,
            "candidateSHA": previous,
            "preservationCapture": "carried-forward-verified",
            "carriedForwardEvidenceSHA256": "c" * 64,
            "carriedForwardEvidencePath": str(self.evidence_path),
            "backupProofSHA256": "6" * 64,
            "backupSHA256": "7" * 64,
            "backupBytes": 321,
            "shadowReceiptSHA256": "8" * 64,
        }
        raw = RETARGET.sign_document(carried, self.key)
        self.write_private(evidence_path, raw)
        journal = dict(self.journal, candidateReleaseSHA=previous, sourceEvidencePath=str(evidence_path),
                       sourceEvidenceSHA256=RETARGET.digest(raw))
        self.write_private(self.journal_path, RETARGET.canonical(journal) + b"\n")
        args = type("Args", (), {"journal": self.journal_path, "source_sha": self.source,
                                 "candidate_sha": self.new_candidate, "transition_id": self.transition,
                                 "evidence": evidence_path})
        bound, evidence, path = RETARGET.verify_source_binding(args, self.key)
        self.assertEqual(bound["candidateReleaseSHA"], previous)
        self.assertEqual(evidence["preservationCapture"], "carried-forward-verified")
        self.assertEqual(path, evidence_path)

    def test_bad_old_signature_or_journal_digest_refuses_retarget(self):
        args = type("Args", (), {"journal": self.journal_path, "source_sha": self.source,
                                 "candidate_sha": self.new_candidate, "transition_id": self.transition,
                                 "evidence": self.evidence_path})
        self.write_private(self.evidence_path, self.evidence_raw.replace(b"\"beforeDigest\":\"b", b"\"beforeDigest\":\"c"))
        with self.assertRaises(SystemExit):
            RETARGET.verify_source_binding(args, self.key)

    def test_bundle_publish_is_atomic_private_and_never_overwrites(self):
        parent = self.root / "recovered-forward"
        parent.mkdir(mode=0o700)
        parent.chmod(0o700)
        target = RETARGET.atomic_bundle(parent, "retarget-fixture", {"one.json": b"one", "two.json": b"two"})
        self.assertEqual((target / "one.json").read_bytes(), b"one")
        self.assertEqual(stat.S_IMODE(target.stat().st_mode), 0o700)
        self.assertEqual(stat.S_IMODE((target / "one.json").stat().st_mode), 0o600)
        with self.assertRaisesRegex(SystemExit, "already exists"):
            RETARGET.atomic_bundle(parent, "retarget-fixture", {"one.json": b"replacement"})
        self.assertEqual((target / "one.json").read_bytes(), b"one")

    def test_carried_forward_evidence_is_explicitly_signed_and_candidate_bound(self):
        value = {
            "schema": RETARGET.EVIDENCE_SCHEMA,
            "transitionId": self.transition,
            "sourceSHA": self.source,
            "candidateSHA": self.new_candidate,
            "preservationCapture": "carried-forward-verified",
            "carriedForwardEvidenceSHA256": RETARGET.digest(self.evidence_raw),
        }
        signed = RETARGET.sign_document(value, self.key)
        verified = RETARGET.verify_hmac(signed, RETARGET.EVIDENCE_SCHEMA, set(value) | {"signature"}, self.key,
                                         "new evidence")
        self.assertEqual(verified["preservationCapture"], "carried-forward-verified")
        self.assertEqual(verified["carriedForwardEvidenceSHA256"], RETARGET.digest(self.evidence_raw))

    def test_database_gate_requires_same_identity_and_exact_schema_47(self):
        args = types.SimpleNamespace(psql=Path("/fixture/psql"))
        environ = {"NORN_DATABASE_URL": "postgres://fixture/norn"}
        good = types.SimpleNamespace(returncode=0, stdout="47|47|0|0|0\n", stderr="")
        with patch.dict(os.environ, {**environ, "NORN_AUDIT_SIGNING_KEY": self.key.decode()}), \
             patch.object(RETARGET.subprocess, "run", return_value=good) as run:
            result = RETARGET.verify_database(args, self.old_evidence, self.key)
        self.assertEqual(result["schemaMax"], 47)
        self.assertNotIn("postgres://fixture/norn", run.call_args.args[0])
        self.assertEqual(run.call_args.kwargs["env"]["PGOPTIONS"], "-c default_transaction_read_only=on")
        for ledger in ("48|48|1|0|0\n", "47|47|0|1|0\n", "46|46|0|0|0\n", "47|47|0|0|2\n"):
            with self.subTest(ledger=ledger), \
                 patch.dict(os.environ, {**environ, "NORN_AUDIT_SIGNING_KEY": self.key.decode()}), \
                 patch.object(RETARGET.subprocess, "run", return_value=types.SimpleNamespace(returncode=0, stdout=ledger, stderr="")), \
                 self.assertRaisesRegex(SystemExit, "not exactly complete"):
                RETARGET.verify_database(args, self.old_evidence, self.key)
        with patch.dict(os.environ, {**environ, "NORN_AUDIT_SIGNING_KEY": self.key.decode()}), \
             self.assertRaisesRegex(SystemExit, "identity differs"):
            RETARGET.verify_database(args, dict(self.old_evidence, databaseIdentity="hmac-sha256:" + "f" * 64), self.key)

    def test_services_must_be_stopped_at_78_and_return_the_exact_fence_sentinel(self):
        api = self.root / "api"
        agent = self.root / "agent"
        for binary in (api, agent):
            binary.write_text("#!/bin/sh\nprintf '%s\\n' 'norn legacy baseline fence active; candidate recovery is required'\nexit 78\n")
            binary.chmod(0o700)
        args = types.SimpleNamespace(launchctl=Path("/fixture/launchctl"), api_binary=api, host_agent_binary=agent)
        output = "PID\tStatus\tLabel\n-\t78\tcom.norn.api\n-\t78\tcom.norn.host-agent\n"
        real_run = RETARGET.subprocess.run
        def run_with_snapshot(command, **kwargs):
            if command[0] == str(args.launchctl):
                return types.SimpleNamespace(returncode=0, stdout=output, stderr="")
            return real_run(command, **kwargs)
        with patch.object(RETARGET.subprocess, "run", side_effect=run_with_snapshot):
            self.assertEqual(len(RETARGET.verify_services(args)), 2)
        for bad in (output.replace("-\t78\tcom.norn.api", "123\t0\tcom.norn.api"),
                    output.replace("-\t78\tcom.norn.host-agent", "-\t0\tcom.norn.host-agent")):
            with patch.object(RETARGET.subprocess, "run", return_value=types.SimpleNamespace(returncode=0, stdout=bad, stderr="")), \
                 self.assertRaises(SystemExit):
                RETARGET.verify_services(args)
        api.write_text("#!/bin/sh\nexit 0\n")
        with patch.object(RETARGET.subprocess, "run", side_effect=run_with_snapshot), \
             self.assertRaisesRegex(SystemExit, "exact exit-78"):
            RETARGET.verify_services(args)

    def test_new_backup_and_shadow_must_be_fresh_and_bound_to_new_candidate(self):
        backup = self.root / "backup.dump"
        backup_raw = b"fixture postgres backup bytes"
        self.write_private(backup, backup_raw)
        now = RETARGET.dt.datetime.now(RETARGET.dt.timezone.utc)
        proof = {"schema": RETARGET.PROOF_SCHEMA, "sourceReleaseSHA": self.source,
                 "databaseIdentity": self.identity, "backupSHA256": RETARGET.digest(backup_raw),
                 "backupBytes": len(backup_raw), "createdAt": now.isoformat().replace("+00:00", "Z")}
        proof_path = self.root / "proof.json"
        self.write_private(proof_path, RETARGET.canonical(proof))
        shadow_path = self.root / "shadow.json"
        shadow = {"schema": RETARGET.SHADOW_SCHEMA,
                  "completedAt": (now + RETARGET.dt.timedelta(seconds=1)).isoformat().replace("+00:00", "Z"),
                  "exitCode": 0, "candidateSHA": self.new_candidate, "shadowLabel": "mini-shadow",
                  "shadowPort": 18800, "runtimeClaim": "passed", "privateRestoreClaim": "passed",
                  "backupSHA256": RETARGET.digest(backup_raw), "backupBytes": len(backup_raw)}
        self.write_private(shadow_path, RETARGET.canonical(shadow))
        args = types.SimpleNamespace(backup_proof=proof_path, backup_artifact=backup, shadow_receipt=shadow_path)
        result = RETARGET.verify_backup_shadow(args, self.source, self.new_candidate, self.identity)
        self.assertEqual(result[3], RETARGET.digest(backup_raw))
        self.write_private(shadow_path, RETARGET.canonical(dict(shadow, candidateSHA=self.old_candidate)))
        with self.assertRaisesRegex(SystemExit, "exact new candidate"):
            RETARGET.verify_backup_shadow(args, self.source, self.new_candidate, self.identity)

    def test_prepare_emits_versioned_signed_bundle_without_rewriting_active_journal_or_evidence(self):
        output = self.root
        proof = self.root / "fresh-proof.json"
        artifact = self.root / "fresh-backup.dump"
        shadow = self.root / "fresh-shadow.json"
        self.write_private(proof, b"proof fixture")
        self.write_private(artifact, b"backup fixture")
        self.write_private(shadow, b"shadow fixture")
        releases = self.root / "releases"
        releases.mkdir()
        for release_sha in (self.source, self.old_candidate, self.new_candidate):
            release_dir = releases / release_sha
            (release_dir / "bin").mkdir(parents=True)
            (release_dir / "release.env").write_text(f"NORN_RELEASE_SHA={release_sha}\n")
            (release_dir / "release.json").write_text("{}\n")
        verifier = self.root / "verify"
        verifier.write_text("#!/bin/sh\nexit 0\n")
        verifier.chmod(0o700)
        api = self.root / "api"
        agent = self.root / "agent"
        api.write_text("#!/bin/sh\nexit 78\n")
        api.chmod(0o700)
        agent.write_text("#!/bin/sh\nexit 78\n")
        agent.chmod(0o700)
        psql = self.root / "psql"
        launchctl = self.root / "launchctl"
        psql.write_text("#!/bin/sh\nexit 0\n")
        launchctl.write_text("#!/bin/sh\nexit 0\n")
        psql.chmod(0o700)
        launchctl.chmod(0o700)
        current_link = self.root / "current"
        current_link.symlink_to(releases / self.source)
        args = [self.new_candidate, "--source-sha", self.source, "--transition-id", self.transition,
                "--journal", str(self.journal_path.resolve()), "--evidence", str(self.evidence_path.resolve()),
                "--backup-proof", str(proof.resolve()), "--backup-artifact", str(artifact.resolve()),
                "--shadow-receipt", str(shadow.resolve()), "--current-link", str(current_link),
                "--releases-root", str(releases.resolve()), "--release-verifier", str(verifier.resolve()),
                "--output-parent", str(output.resolve()), "--api-binary", str(api.resolve()),
                "--host-agent-binary", str(agent.resolve())]
        with patch.dict(os.environ, {"NORN_AUDIT_SIGNING_KEY": self.key.decode()}), \
             patch.object(RETARGET, "verify_source_binding", return_value=(self.journal, self.old_evidence, self.evidence_path)), \
             patch.object(RETARGET, "verify_source_release", return_value=releases / self.new_candidate), \
             patch.object(RETARGET, "verify_database", return_value={"databaseIdentity": self.identity, "schemaMax": 47, "schemaRows": 47, "futureRows": 0, "unknownRows": 0, "otherClientBackends": 0}), \
             patch.object(RETARGET, "verify_backup_shadow", return_value=({}, {}, "c" * 64, "d" * 64, 14)), \
             patch.object(RETARGET, "verify_services", return_value={"com.norn.api": {"pid": "-", "status": "78"}, "com.norn.host-agent": {"pid": "-", "status": "78"}}):
            self.assertEqual(RETARGET.run(args), 0)
            bundle_bytes = {name: (output / f"retarget-{self.new_candidate}" / name).read_bytes()
                            for name in ("retarget.json", "transition.json", "source-evidence.json", "previous-transition.json")}
            self.assertEqual(RETARGET.run(args), 0)
            self.assertEqual(bundle_bytes, {name: (output / f"retarget-{self.new_candidate}" / name).read_bytes()
                                           for name in bundle_bytes})
        bundle = output / f"retarget-{self.new_candidate}"
        self.assertTrue((bundle / "retarget.json").is_file())
        self.assertTrue((bundle / "transition.json").is_file())
        self.assertTrue((bundle / "source-evidence.json").is_file())
        self.assertEqual(self.journal_path.read_bytes(), self.journal_raw)
        self.assertEqual(self.evidence_path.read_bytes(), self.evidence_raw)
        tuple_raw = (bundle / "retarget.json").read_bytes()
        tuple_value = RETARGET.verify_hmac(tuple_raw, RETARGET.RETARGET_SCHEMA,
                                           set(RETARGET.strict_json(tuple_raw, "retarget tuple")), self.key,
                                           "retarget tuple")
        self.assertEqual(tuple_value["preservationCapture"], "carried-forward-verified")
        self.assertEqual(tuple_value["candidateSHA"], self.new_candidate)
        self.assertEqual(stat.S_IMODE((bundle / "transition.json").stat().st_mode), 0o600)
        self.assertEqual((bundle / "previous-transition.json").read_bytes(), self.journal_raw)

        consume_args = [str(bundle), "--candidate-sha", self.new_candidate,
                        "--journal", str(self.journal_path.resolve()), "--releases-root", str(releases.resolve()),
                        "--release-verifier", str(verifier.resolve()), "--current-link", str(current_link),
                        "--backup-proof", str(proof.resolve()), "--backup-artifact", str(artifact.resolve()),
                        "--shadow-receipt", str(shadow.resolve()), "--psql", str(psql.resolve()),
                        "--launchctl", str(launchctl.resolve()), "--api-binary", str(api.resolve()),
                        "--host-agent-binary", str(agent.resolve())]
        parsed = CONSUMER.parser().parse_args(consume_args)
        for filename in ("transition.json", "source-evidence.json"):
            path = bundle / filename
            original = path.read_bytes()
            path.write_bytes(original + b" ")
            with self.subTest(tampered=filename), self.assertRaisesRegex(SystemExit, "differ from the signed retarget tuple"):
                CONSUMER.verify_bundle(parsed, self.key)
            path.write_bytes(original)
        db_proof = {"databaseIdentity": self.identity, "schemaMax": 47, "schemaRows": 47,
                    "futureRows": 0, "unknownRows": 0, "otherClientBackends": 0}
        # A crash after archiving but before replace is retryable and keeps the
        # byte-exact old active journal until the next successful attempt.
        with patch.dict(os.environ, {"NORN_DATABASE_URL": "postgres://fixture/norn", "NORN_AUDIT_SIGNING_KEY": self.key.decode()}), \
             patch.object(CONSUMER.RETARGET, "verify_source_release", return_value=releases / self.new_candidate), \
             patch.object(CONSUMER.RETARGET, "verify_database", return_value=db_proof), \
             patch.object(CONSUMER.RETARGET, "verify_services", return_value=tuple_value["services"]), \
             patch.object(CONSUMER.RETARGET, "verify_backup_shadow", return_value=({}, {}, "c" * 64, "d" * 64, 14)), \
             patch.object(CONSUMER.os, "replace", side_effect=OSError("simulated interruption")), \
             self.assertRaisesRegex(OSError, "simulated interruption"):
            CONSUMER.perform(parsed)
        archive = self.journal_path.with_name(f"transition.json.previous-{self.old_candidate}-{RETARGET.digest(self.journal_raw)}")
        self.assertEqual(archive.read_bytes(), self.journal_raw)
        self.assertEqual(self.journal_path.read_bytes(), self.journal_raw)

        with patch.dict(os.environ, {"NORN_DATABASE_URL": "postgres://fixture/norn", "NORN_AUDIT_SIGNING_KEY": self.key.decode()}), \
             patch.object(CONSUMER.RETARGET, "verify_source_release", return_value=releases / self.new_candidate), \
             patch.object(CONSUMER.RETARGET, "verify_database", return_value=db_proof), \
             patch.object(CONSUMER.RETARGET, "verify_services", return_value=tuple_value["services"]), \
             patch.object(CONSUMER.RETARGET, "verify_backup_shadow", return_value=({}, {}, "c" * 64, "d" * 64, 14)):
            self.assertEqual(CONSUMER.perform(parsed), 0)
            self.assertEqual(CONSUMER.perform(parsed), 0)
        self.assertEqual(self.journal_path.read_bytes(), (bundle / "transition.json").read_bytes())
        self.assertEqual(self.evidence_path.read_bytes(), self.evidence_raw)


if __name__ == "__main__":
    unittest.main()
