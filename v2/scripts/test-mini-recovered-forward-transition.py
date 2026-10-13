#!/usr/bin/env python3
"""Focused local contract tests for the recovered-forward Mini wrapper."""

import hashlib
import hmac
import importlib.machinery
import importlib.util
import json
import os
import stat
import tempfile
import types
import unittest
from pathlib import Path
from unittest.mock import patch

SCRIPT = Path(__file__).with_name("mini-recovered-forward-transition")
LOADER = importlib.machinery.SourceFileLoader("mini_recovered_forward_transition", str(SCRIPT))
SPEC = importlib.util.spec_from_loader(LOADER.name, LOADER)
WRAPPER = importlib.util.module_from_spec(SPEC)
assert SPEC and SPEC.loader
SPEC.loader.exec_module(WRAPPER)


class RecoveredForwardWrapperTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.key = b"audit-key-for-wrapper-tests-32bytes!"
        self.source = "a" * 40
        self.candidate = "b" * 40
        self.transition = "mini-recovery-test"
        self.before = "c" * 64
        self.evidence = {
            "schema": WRAPPER.EVIDENCE_SCHEMA,
            "transitionId": self.transition,
            "sourceSHA": self.source,
            "candidateSHA": self.candidate,
            "databaseIdentity": "hmac-sha256:" + "d" * 64,
            "recoveryFenceSHA256": "e" * 64,
            "adoptionReceiptSHA256": "f" * 64,
            "incidentBindingSHA256": "1" * 64,
            "ledgerSHA256": "2" * 64,
            "reconciliationSHA256": "3" * 64,
            "backupProofSHA256": "4" * 64,
            "backupSHA256": "5" * 64,
            "backupBytes": 123,
            "shadowReceiptSHA256": "6" * 64,
            "beforeDigest": self.before,
        }

    def tearDown(self):
        self.tmp.cleanup()

    def test_receipt_is_exactly_bound_and_hmac_signed(self):
        raw = WRAPPER.build_receipt(self.evidence, "7" * 64, self.candidate, self.before, self.key)
        receipt = WRAPPER.verify_hmac_document(raw, WRAPPER.RECEIPT_SCHEMA, self.key,
                                                "test receipt", WRAPPER.RECEIPT_FIELDS)
        self.assertEqual(set(receipt), WRAPPER.RECEIPT_FIELDS)
        self.assertEqual(receipt["transitionId"], self.transition)
        self.assertEqual(receipt["candidateSHA"], self.candidate)
        self.assertEqual(receipt["sourceEvidenceSHA256"], "7" * 64)
        self.assertEqual(receipt["beforeDigest"], receipt["afterDigest"])
        self.assertIs(receipt["preservationMatched"], True)
        self.assertTrue(receipt["finalizedAt"].endswith("Z"))

    def test_changed_preservation_digest_cannot_produce_receipt(self):
        with self.assertRaisesRegex(SystemExit, "preservation digest changed"):
            WRAPPER.build_receipt(self.evidence, "7" * 64, self.candidate, "8" * 64, self.key)

    def test_prepared_or_unjournaled_transition_requires_fresh_backup_and_shadow(self):
        self.assertTrue(WRAPPER.requires_fresh_inputs(False, None))
        self.assertTrue(WRAPPER.requires_fresh_inputs(True, {"state": "prepared"}))
        self.assertFalse(WRAPPER.requires_fresh_inputs(True, {"state": "candidate-activation-started"}))
        source = SCRIPT.read_text()
        verification = source.index("enforce_freshness=requires_fresh_inputs")
        mutation = source.index('invoke_platform(platform_script, "recovered-forward-controller"')
        self.assertLess(verification, mutation)

    def test_receipt_rejects_duplicate_fields_and_wrong_hmac(self):
        raw = WRAPPER.signed_document({"schema": WRAPPER.RECEIPT_SCHEMA, "x": 1}, self.key)
        duplicate = raw[:-1] + b',"x":2}'
        with self.assertRaisesRegex(SystemExit, "duplicate JSON field"):
            WRAPPER.verify_hmac_document(duplicate, WRAPPER.RECEIPT_SCHEMA, self.key, "test")
        with self.assertRaisesRegex(SystemExit, "HMAC signature"):
            WRAPPER.verify_hmac_document(raw, WRAPPER.RECEIPT_SCHEMA, b"z" * 32, "test")

    def test_unjournaled_transition_requires_exact_source_database_and_before_state(self):
        stable = {"apps": [], "services": []}
        fake = types.SimpleNamespace(stable_state=lambda _api, _token: stable,
                                     digest=lambda state: WRAPPER.digest(state))
        with patch.object(WRAPPER, "fetch_json", side_effect=[{"sourceSha": self.source},
                         {"databaseIdentity": self.evidence["databaseIdentity"]}]), \
             patch.object(WRAPPER, "load_preservation_module", return_value=fake):
            WRAPPER.validate_unjournaled_source("http://127.0.0.1:1", "t",
                                                {"databaseIdentity": self.evidence["databaseIdentity"]},
                                                {**self.evidence, "beforeDigest": WRAPPER.digest(stable)})

        for version, schema, state, message in [
            ({"sourceSha": self.candidate}, {"databaseIdentity": self.evidence["databaseIdentity"]}, stable, "source release"),
            ({"sourceSha": self.source}, {"databaseIdentity": "hmac-sha256:" + "9" * 64}, stable, "database identity"),
            ({"sourceSha": self.source}, {"databaseIdentity": self.evidence["databaseIdentity"]}, {"apps": ["changed"]}, "state differs"),
        ]:
            fake = types.SimpleNamespace(stable_state=lambda _api, _token, value=state: value,
                                         digest=lambda value: WRAPPER.digest(value))
            with patch.object(WRAPPER, "fetch_json", side_effect=[version, schema]), \
                 patch.object(WRAPPER, "load_preservation_module", return_value=fake), \
                 self.assertRaisesRegex(SystemExit, message):
                WRAPPER.validate_unjournaled_source("http://127.0.0.1:1", "t",
                                                    {"databaseIdentity": self.evidence["databaseIdentity"]}, self.evidence)

    def test_journal_must_bind_exact_evidence_and_transition(self):
        journal = self.root / "transition.json"
        record = {
            "schema": WRAPPER.TRANSITION_SCHEMA,
            "transitionID": self.transition,
            "sourceSHA": self.source,
            "candidateReleaseSHA": self.candidate,
            "sourceEvidencePath": str(journal.with_name(journal.name + ".source-evidence.json")),
            "sourceEvidenceSHA256": "7" * 64,
            "state": "candidate-preservation-pending",
        }
        journal.write_bytes(WRAPPER.canonical(record))
        journal.chmod(0o600)
        evidence_path = journal.with_name(journal.name + ".source-evidence.json")
        self.assertEqual(WRAPPER.load_journal(journal, self.transition, self.source, self.candidate, "7" * 64,
                                              evidence_path), record)
        with self.assertRaisesRegex(SystemExit, "sourceEvidenceSHA256"):
            WRAPPER.load_journal(journal, self.transition, self.source, self.candidate, "8" * 64,
                                 evidence_path)

    def test_atomic_private_create_is_owner_only_and_never_overwrites(self):
        directory = self.root / "recovered-forward"
        directory.mkdir(mode=0o700)
        directory.chmod(0o700)
        target = directory / "receipt.json"
        WRAPPER.atomic_private_create(target, b"receipt")
        self.assertEqual(target.read_bytes(), b"receipt")
        self.assertEqual(stat.S_IMODE(target.stat().st_mode), 0o600)
        with self.assertRaisesRegex(SystemExit, "already exists"):
            WRAPPER.atomic_private_create(target, b"replacement")
        self.assertEqual(target.read_bytes(), b"receipt")

    def test_preservation_digest_uses_the_existing_mini_local_contract(self):
        helper = WRAPPER.load_preservation_module()
        state = {"health": {"status": "ok"}, "apps": [{"name": "mini-app"}],
                 "services": {"services": [{"name": "nomad"}]}}
        self.assertEqual(helper.digest(state), WRAPPER.digest(state))
        self.assertFalse(hasattr(WRAPPER, "post_json"), "wrapper must leave API finalization to platform-upgrade")
        self.assertNotIn("/api/v1/platform/recovered-forward/finalize", SCRIPT.read_text())

    def test_production_controller_is_from_the_exact_candidate_not_the_old_host_path(self):
        release = self.root / "norn" / "releases" / self.candidate
        bin_dir = release / "bin"
        bin_dir.mkdir(parents=True)
        helper = bin_dir / "mini-recovered-forward-transition"
        helper.write_text("fixture helper")
        controller = bin_dir / "platform-upgrade"
        controller.write_text("fixture candidate controller")
        controller.chmod(0o700)
        (release / "release.env").write_text(f"NORN_RELEASE_SHA={self.candidate}\n")
        # The installed source controller remains the deployment target, but
        # the signed candidate controller must own the new transition logic.
        old_controller = self.root / "host" / "bin" / "platform-upgrade"
        old_controller.parent.mkdir(parents=True)
        old_controller.write_text("old source controller")
        with patch.dict(os.environ, {"NORN_PLATFORM_SCRIPT_BIN": str(old_controller)}):
            selected = WRAPPER.resolve_platform_script(self.candidate, self.root, helper)
        self.assertEqual(selected, controller.resolve())

    def test_production_controller_rejects_wrong_release_and_symlink(self):
        release = self.root / "norn" / "releases" / self.candidate
        bin_dir = release / "bin"
        bin_dir.mkdir(parents=True)
        helper = bin_dir / "mini-recovered-forward-transition"
        helper.write_text("fixture helper")
        controller = bin_dir / "platform-upgrade"
        controller.write_text("fixture candidate controller")
        controller.chmod(0o700)
        (release / "release.env").write_text(f"NORN_RELEASE_SHA={self.candidate}\n")
        with self.assertRaisesRegex(SystemExit, "not an immutable installed release"):
            WRAPPER.resolve_platform_script("a" * 40, self.root, helper)
        controller.unlink()
        controller.symlink_to(old_controller := (self.root / "old-controller"))
        old_controller.write_text("unexpected controller")
        old_controller.chmod(0o700)
        with self.assertRaisesRegex(SystemExit, "executable regular file"):
            WRAPPER.resolve_platform_script(self.candidate, self.root, helper)

    def test_normal_wrapper_cannot_resume_fenced_candidate_with_repair_controller(self):
        candidate = self.root / "norn" / "releases" / self.candidate
        candidate_bin = candidate / "bin"
        candidate_bin.mkdir(parents=True)
        (candidate / "release.env").write_text(f"NORN_RELEASE_SHA={self.candidate}\n")
        repair_sha = "c" * 40
        repair_bin = self.root / "norn" / "releases" / repair_sha / "bin"
        repair_bin.mkdir(parents=True)
        repair_helper = repair_bin / "mini-recovered-forward-transition"
        repair_helper.write_text("signed repair release fixture")
        (repair_bin.parent / "release.env").write_text(f"NORN_RELEASE_SHA={repair_sha}\n")
        controller = repair_bin / "platform-upgrade"
        controller.write_text("new controller fixture")
        controller.chmod(0o700)
        with self.assertRaisesRegex(SystemExit, "exact candidate release"):
            WRAPPER.resolve_platform_script(self.candidate, self.root, repair_helper)

    def test_test_controller_override_stays_hermetically_gated(self):
        controller = self.root / "controller"
        controller.write_text("fixture")
        controller.chmod(0o700)
        with patch.dict(os.environ, {}, clear=True):
            with self.assertRaisesRegex(SystemExit, "hermetic test harness"):
                WRAPPER.resolve_platform_script(self.candidate, self.root, test_path=str(controller))
        with patch.dict(os.environ, {"NORN_RECOVERED_FORWARD_TEST_HOOKS": "1"}):
            self.assertEqual(WRAPPER.resolve_platform_script(self.candidate, self.root,
                                                               test_path=str(controller)), controller)


if __name__ == "__main__":
    unittest.main()
