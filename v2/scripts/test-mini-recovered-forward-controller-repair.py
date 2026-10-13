#!/usr/bin/env python3
"""Negative tests for the controller substitution authorization boundary."""

import importlib.machinery
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch


SCRIPT = Path(__file__).with_name("mini-recovered-forward-controller-repair")
LOADER = importlib.machinery.SourceFileLoader("mini_recovered_forward_controller_repair", str(SCRIPT))
SPEC = importlib.util.spec_from_loader(LOADER.name, LOADER)
assert SPEC and SPEC.loader
REPAIR = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(REPAIR)


class ControllerRepairAuthorizationTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.directory = Path(self.temp.name).resolve() / "generation"
        self.directory.mkdir(mode=0o700)
        self.path = self.directory / "controller-repair.json"
        self.key = b"test-audit-key-longer-than-thirty-two-bytes"
        self.expected = {
            "schema": REPAIR.SCHEMA,
            "controllerSHA": "c" * 40,
            "candidateSHA": "b" * 40,
            "sourceSHA": "a" * 40,
            "transitionID": "mini-v3-20261013-3602e7e3",
            "pointerSHA256": "1" * 64,
            "manifestSHA256": "2" * 64,
            "journalSHA256": "3" * 64,
            "evidenceSHA256": "4" * 64,
            "generationID": "gen-example",
            "currentReleaseSHA": "a" * 40,
        }

    def tearDown(self):
        self.temp.cleanup()

    def test_valid_authorization_is_private_and_cannot_be_reissued(self):
        REPAIR.create(self.path, self.expected, self.key)
        self.assertEqual(self.path.stat().st_mode & 0o777, 0o600)
        REPAIR.verify_authorization(self.path, self.expected, self.key)
        with self.assertRaisesRegex(ValueError, "already exists"):
            REPAIR.create(self.path, self.expected, self.key)

    def test_journal_advance_or_controller_change_invalidates_authorization(self):
        REPAIR.create(self.path, self.expected, self.key)
        for field, value in (("journalSHA256", "5" * 64), ("controllerSHA", "d" * 40),
                             ("candidateSHA", "e" * 40), ("pointerSHA256", "6" * 64),
                             ("currentReleaseSHA", "b" * 40)):
            with self.subTest(field=field), self.assertRaisesRegex(ValueError, "stale"):
                REPAIR.verify_authorization(self.path, dict(self.expected, **{field: value}), self.key)

    def test_wrong_key_and_tampered_document_are_rejected(self):
        REPAIR.create(self.path, self.expected, self.key)
        with self.assertRaisesRegex(ValueError, "HMAC"):
            REPAIR.verify_authorization(self.path, self.expected, b"another-audit-key-longer-than-thirty-two")
        changed = json.loads(self.path.read_bytes())
        changed["sourceSHA"] = "d" * 40
        self.path.write_bytes(REPAIR.canonical(changed))
        with self.assertRaisesRegex(ValueError, "HMAC"):
            REPAIR.verify_authorization(self.path, self.expected, self.key)

    def test_caller_cannot_replace_configured_signature_verifier(self):
        config = self.directory / ".config/norn"
        config.mkdir(parents=True)
        verifier = self.directory / "verify-release"
        verifier.write_text("#!/bin/sh\nexit 0\n")
        verifier.chmod(0o700)
        file = config / "release.env"
        file.write_text(f"NORN_RELEASE_VERIFY_HOOK={verifier}\n")
        file.chmod(0o600)
        self.assertEqual(REPAIR.configured_verifier(self.directory), verifier)
        with patch.dict("os.environ", {"NORN_RELEASE_VERIFY_HOOK": "/bin/true"}):
            with self.assertRaisesRegex(ValueError, "differs"):
                REPAIR.configured_verifier(self.directory)

    def test_activation_predecessor_requires_exact_journal_and_signed_release(self):
        releases = self.directory / "releases"
        source, candidate, predecessor = "a" * 40, "b" * 40, "d" * 40
        journal = {"retargetedFromCandidateSHA": predecessor}
        verifier = self.directory / "verifier"
        with patch.object(REPAIR, "release", return_value=releases / predecessor) as verified:
            allowed = REPAIR.allowed_current_releases(
                "candidate-activation-started", journal, releases, source, candidate, verifier)
        self.assertEqual(allowed, {releases / source, releases / candidate, releases / predecessor})
        verified.assert_called_once_with(releases, predecessor, verifier)
        with self.assertRaisesRegex(ValueError, "before candidate activation"):
            REPAIR.allowed_current_releases("schema-migrated", journal, releases, source, candidate, verifier)
        with self.assertRaisesRegex(ValueError, "predecessor is invalid"):
            REPAIR.allowed_current_releases("candidate-activation-started",
                                            {"retargetedFromCandidateSHA": source},
                                            releases, source, candidate, verifier)
        with patch.object(REPAIR, "release", side_effect=ValueError("signature failed")):
            with self.assertRaisesRegex(ValueError, "signature failed"):
                REPAIR.allowed_current_releases("candidate-activation-started", journal,
                                                releases, source, candidate, verifier)


if __name__ == "__main__":
    unittest.main()
