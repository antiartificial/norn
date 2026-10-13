#!/usr/bin/env python3
"""Hermetic integration tests for recovered-forward generation preparation."""

from __future__ import annotations

import hashlib
import hmac
import importlib.machinery
import importlib.util
import json
import os
import fcntl
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("mini-recovered-forward-generation-prepare")
EVIDENCE_FIXTURE = Path(__file__).with_name("test-mini-recovered-forward-evidence.py")
LOADER = importlib.machinery.SourceFileLoader("norn_evidence_fixture", str(EVIDENCE_FIXTURE))
SPEC = importlib.util.spec_from_loader(LOADER.name, LOADER)
assert SPEC and SPEC.loader
EVIDENCE_TESTS = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(EVIDENCE_TESTS)
KEY = EVIDENCE_TESTS.KEY
HISTORICAL_KEY = b"fixture-historical-audit-signing-key"
WRONG_KEY = b"fixture-wrong-audit-signing-key-000"
SOURCE = EVIDENCE_TESTS.SOURCE
OLD_CANDIDATE = EVIDENCE_TESTS.CANDIDATE
NEW_CANDIDATE = "e" * 40
OLD_TRANSITION = EVIDENCE_TESTS.TRANSITION
NEW_TRANSITION = "mini-v3-forward-next"


def dump(value: dict) -> bytes:
    return (json.dumps(value, sort_keys=True, separators=(",", ":")) + "\n").encode()


def sign(value: dict, key: bytes = KEY) -> bytes:
    unsigned = dict(value)
    schema = unsigned["schema"]
    unsigned["signature"] = "hmac-sha256:" + hmac.new(
        key, schema.encode() + b"\0" + json.dumps(value, sort_keys=True, separators=(",", ":")).encode(),
        hashlib.sha256,
    ).hexdigest()
    return dump(unsigned)


def private(path: Path, raw: bytes) -> Path:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(raw)
    path.chmod(0o600)
    return path


class GenerationPrepareTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name).resolve()
        self.fixture = EVIDENCE_TESTS.RecoveredForwardEvidenceTest()
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        self.fixture.artifact.write_bytes(b"large hermetic backup fixture\n" * 50000)
        self.fixture.artifact.chmod(0o600)
        self.fixture.write_inputs()
        self.home = self.fixture.home
        self.root = self.home / ".config/norn/recovered-forward"
        self.root.mkdir(parents=True, mode=0o700)
        self.root.chmod(0o700)
        self.releases = self.base / "releases"
        for sha in (SOURCE, OLD_CANDIDATE, NEW_CANDIDATE):
            release = self.releases / sha
            release.mkdir(parents=True)
            (release / "release.env").write_text(f"NORN_RELEASE_SHA={sha}\n")
        self.current = self.home / "norn/current"
        self.current.parent.mkdir(parents=True)
        # Historical validation must not require the old candidate to remain live.
        self.current.symlink_to(self.releases / SOURCE)
        self.old_journal = self.root / "transition.json"
        self.old_evidence = self.root / "transition.json.source-evidence.json"
        self.old_receipt = self.root / "preservation-receipt.json"
        self.old_evidence.write_bytes(self.fixture.evidence.read_bytes())
        self.old_evidence.chmod(0o600)
        receipt = {
            "schema": "norn.recovered-forward-preservation/v1",
            "transitionId": OLD_TRANSITION,
            "candidateSHA": OLD_CANDIDATE,
            "sourceEvidenceSHA256": hashlib.sha256(self.old_evidence.read_bytes()).hexdigest(),
            "beforeDigest": "4" * 64,
            "afterDigest": "4" * 64,
            "preservationMatched": True,
            "finalizedAt": "2026-10-11T12:03:00Z",
        }
        receipt_raw = sign(receipt)
        private(self.old_receipt, receipt_raw)
        self.old_proof = self.fixture.proof
        self.old_artifact = self.fixture.artifact
        self.old_shadow = self.fixture.shadow
        journal = {
            "schema": "norn.recovered-forward-transition/v1",
            "state": "candidate-promoted",
            "transitionID": OLD_TRANSITION,
            "sourceSHA": SOURCE,
            "candidateReleaseSHA": OLD_CANDIDATE,
            "sourceEvidencePath": str(self.old_evidence),
            "sourceEvidenceSHA256": hashlib.sha256(self.old_evidence.read_bytes()).hexdigest(),
            "backupProofPath": str(self.old_proof),
            "backupArtifactPath": str(self.old_artifact),
            "shadowReceiptPath": str(self.old_shadow),
            "preservationReceiptPath": str(self.old_receipt),
            "preservationReceiptSHA256": hashlib.sha256(receipt_raw).hexdigest(),
        }
        private(self.old_journal, dump(journal))

        # Create a distinct exact new v2 evidence tuple and matching shadow receipt.
        new_shadow_value = json.loads(self.fixture.shadow.read_text())
        new_shadow_value["candidateSHA"] = NEW_CANDIDATE
        self.new_shadow = private(self.base / "new-shadow.json", dump(new_shadow_value))
        new_evidence_value = json.loads(self.fixture.evidence.read_text())
        new_evidence_value.pop("signature")
        new_evidence_value.update({
            "transitionId": NEW_TRANSITION,
            "candidateSHA": NEW_CANDIDATE,
            "shadowReceiptSHA256": hashlib.sha256(self.new_shadow.read_bytes()).hexdigest(),
        })
        self.new_evidence = private(self.base / "new-evidence.json", sign(new_evidence_value))
        self.env = {**os.environ, "HOME": str(self.home), "NORN_AUDIT_SIGNING_KEY": KEY.decode()}
        self.args = [
            str(SCRIPT), "prepare", "--root", str(self.root), "--transition-id", NEW_TRANSITION,
            "--source-sha", SOURCE, "--candidate-sha", NEW_CANDIDATE,
            "--source-evidence", str(self.new_evidence), "--backup-proof", str(self.fixture.proof),
            "--backup-artifact", str(self.fixture.artifact), "--shadow-receipt", str(self.new_shadow),
            "--current-link", str(self.current), "--releases-root", str(self.releases),
        ]

    def invoke(self):
        return subprocess.run(self.args, env=self.env, capture_output=True, text=True, timeout=15)

    def use_historical_predecessor(self):
        # Preserve the current adoption receipt. The predecessor keeps its
        # historical receipt as a separate immutable, digest-addressed file;
        # the new generation continues to bind the current active receipt.
        adoption_path = self.home / ".config/norn/.norn-recovered-baseline-adoption-v2.json"
        adoption = json.loads(adoption_path.read_bytes())
        adoption.pop("signature")
        archived_adoption = self.home / ".config/norn/.norn-recovered-baseline-adoption-v2.json.previous-fixture"
        archived_raw = sign(adoption, HISTORICAL_KEY)
        private(archived_adoption, archived_raw)
        value = json.loads(self.old_evidence.read_bytes())
        value.pop("signature")
        value["adoptionReceiptSHA256"] = hashlib.sha256(archived_raw).hexdigest()
        evidence_raw = sign(value, HISTORICAL_KEY)
        private(self.old_evidence, evidence_raw)
        receipt = json.loads(self.old_receipt.read_bytes())
        receipt.pop("signature")
        receipt["sourceEvidenceSHA256"] = hashlib.sha256(evidence_raw).hexdigest()
        receipt_raw = sign(receipt, HISTORICAL_KEY)
        private(self.old_receipt, receipt_raw)
        journal = json.loads(self.old_journal.read_bytes())
        journal["sourceEvidenceSHA256"] = hashlib.sha256(evidence_raw).hexdigest()
        journal["preservationReceiptSHA256"] = hashlib.sha256(receipt_raw).hexdigest()
        private(self.old_journal, dump(journal))
        self.env["NORN_AUDIT_HISTORICAL_SIGNING_KEY"] = HISTORICAL_KEY.decode()

    def test_prepares_verified_generation_with_pointer_last_and_is_idempotent(self):
        fixed_before = {path: hashlib.sha256(path.read_bytes()).hexdigest() for path in
                        (self.old_journal, self.old_evidence, self.old_receipt)}
        first = self.invoke()
        self.assertEqual(first.returncode, 0, first.stderr)
        result = json.loads(first.stdout)
        generation = self.root / "generations" / result["generationID"]
        self.assertEqual(result["journalPath"], str(generation / "transition.json"))
        self.assertTrue((self.root / "active-generation.json").is_file())
        self.assertEqual((generation / "transition.json.source-evidence.json").read_bytes(), self.new_evidence.read_bytes())
        self.assertEqual((generation / "backup-proof.json").stat().st_mode & 0o777, 0o600)
        self.assertEqual(generation.stat().st_mode & 0o777, 0o700)
        manifest = json.loads((generation / "generation.json").read_text())
        self.assertGreater(manifest["backupArtifactBytes"], 64 * 1024)
        self.assertEqual(manifest["backupArtifactPath"], str(self.fixture.artifact))
        self.assertFalse((generation / "backup-artifact.bin").exists())
        self.assertEqual(manifest["predecessorJournalSHA256"], fixed_before[self.old_journal])
        self.assertEqual(manifest["predecessorEvidenceSHA256"], fixed_before[self.old_evidence])
        self.assertEqual(manifest["predecessorReceiptSHA256"], fixed_before[self.old_receipt])
        second = self.invoke()
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(json.loads(second.stdout), result)
        self.assertEqual({path: hashlib.sha256(path.read_bytes()).hexdigest() for path in fixed_before}, fixed_before)

    def test_invalid_new_evidence_never_activates_pointer(self):
        evidence = json.loads(self.new_evidence.read_text())
        evidence["candidateSHA"] = "f" * 40
        private(self.new_evidence, dump(evidence))
        result = self.invoke()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("did not verify", result.stderr)
        self.assertFalse((self.root / "active-generation.json").exists())

    def test_old_fixed_journal_bytes_and_terminal_state_are_required(self):
        journal = json.loads(self.old_journal.read_text())
        journal["state"] = "candidate-postflight-failed"
        private(self.old_journal, dump(journal))
        result = self.invoke()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("candidate-promoted", result.stderr)
        self.assertFalse((self.root / "active-generation.json").exists())

    def test_predecessor_receipt_hmac_and_unexpected_orphan_fail_closed(self):
        receipt = json.loads(self.old_receipt.read_text())
        receipt["afterDigest"] = "5" * 64
        tampered_receipt = dump(receipt)
        private(self.old_receipt, tampered_receipt)
        journal = json.loads(self.old_journal.read_text())
        journal["preservationReceiptSHA256"] = hashlib.sha256(tampered_receipt).hexdigest()
        private(self.old_journal, dump(journal))
        result = self.invoke()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("signature does not verify", result.stderr)
        self.assertFalse((self.root / "active-generation.json").exists())

        original_receipt = {
            "schema": "norn.recovered-forward-preservation/v1",
            "transitionId": OLD_TRANSITION,
            "candidateSHA": OLD_CANDIDATE,
            "sourceEvidenceSHA256": hashlib.sha256(self.old_evidence.read_bytes()).hexdigest(),
            "beforeDigest": "4" * 64,
            "afterDigest": "4" * 64,
            "preservationMatched": True,
            "finalizedAt": "2026-10-11T12:03:00Z",
        }
        restored_receipt = sign(original_receipt)
        private(self.old_receipt, restored_receipt)
        journal["preservationReceiptSHA256"] = hashlib.sha256(restored_receipt).hexdigest()
        private(self.old_journal, dump(journal))
        generations = self.root / "generations"
        generations.mkdir(mode=0o700)
        (generations / "unexpected-orphan").mkdir(mode=0o700)
        result = self.invoke()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("unexpected generation orphan", result.stderr)
        self.assertFalse((self.root / "active-generation.json").exists())

    def test_wrong_live_link_is_accepted_only_for_historical_gate(self):
        # The live source differs from the predecessor candidate; the explicit historical gate
        # validates signed historical evidence and immutable releases without consulting it.
        result = self.invoke()
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_mixed_historical_and_active_keys_succeed_and_preserve_predecessor_bytes(self):
        self.use_historical_predecessor()
        archived_adoption = self.home / ".config/norn/.norn-recovered-baseline-adoption-v2.json.previous-fixture"
        current_adoption = self.home / ".config/norn/.norn-recovered-baseline-adoption-v2.json"
        predecessor_bytes = {path: path.read_bytes() for path in
                             (self.old_journal, self.old_evidence, self.old_receipt,
                              archived_adoption, current_adoption)}
        result = self.invoke()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual({path: path.read_bytes() for path in predecessor_bytes}, predecessor_bytes)
        generation = self.root / "generations" / json.loads(result.stdout)["generationID"]
        manifest = json.loads((generation / "generation.json").read_bytes())
        self.assertEqual(manifest["predecessorReceiptSHA256"], hashlib.sha256(predecessor_bytes[self.old_receipt]).hexdigest())
        resolver = Path(__file__).with_name("mini-recovered-forward-generation-resolve")
        active_only_env = {**self.env, "NORN_AUDIT_SIGNING_KEY": KEY.decode()}
        active_only_env.pop("NORN_AUDIT_HISTORICAL_SIGNING_KEY", None)
        resolved = subprocess.run([str(resolver), "--root", str(self.root)], env=active_only_env,
                                  capture_output=True, text=True, timeout=15)
        self.assertEqual(resolved.returncode, 0, resolved.stderr)
        self.assertEqual(json.loads(resolved.stdout)["selection"], "generation")

    def test_missing_exact_archived_adoption_fails_before_generation_or_pointer(self):
        self.use_historical_predecessor()
        archived_adoption = self.home / ".config/norn/.norn-recovered-baseline-adoption-v2.json.previous-fixture"
        archived_adoption.write_bytes(archived_adoption.read_bytes() + b" ")
        result = self.invoke()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("no exact archived adoption receipt", result.stderr)
        self.assertFalse((self.root / "active-generation.json").exists())
        self.assertFalse((self.root / "generations").exists())

    def test_missing_or_wrong_historical_key_fails_before_pointer_creation(self):
        self.use_historical_predecessor()
        self.env.pop("NORN_AUDIT_HISTORICAL_SIGNING_KEY")
        missing = self.invoke()
        self.assertNotEqual(missing.returncode, 0)
        self.assertIn("signature does not verify", missing.stderr)
        self.assertFalse((self.root / "active-generation.json").exists())
        self.assertFalse((self.root / "generations").exists())

        self.env["NORN_AUDIT_HISTORICAL_SIGNING_KEY"] = WRONG_KEY.decode()
        wrong = self.invoke()
        self.assertNotEqual(wrong.returncode, 0)
        self.assertIn("signature does not verify", wrong.stderr)
        self.assertFalse((self.root / "active-generation.json").exists())
        self.assertFalse((self.root / "generations").exists())

    def test_tampered_predecessor_receipt_fails_with_historical_key(self):
        self.use_historical_predecessor()
        receipt = json.loads(self.old_receipt.read_bytes())
        receipt["afterDigest"] = "5" * 64
        raw = dump(receipt)
        private(self.old_receipt, raw)
        journal = json.loads(self.old_journal.read_bytes())
        journal["preservationReceiptSHA256"] = hashlib.sha256(raw).hexdigest()
        private(self.old_journal, dump(journal))
        result = self.invoke()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("signature does not verify", result.stderr)
        self.assertFalse((self.root / "active-generation.json").exists())
        self.assertFalse((self.root / "generations").exists())

    def test_wrong_active_key_still_rejects_current_adoption_binding(self):
        self.use_historical_predecessor()
        self.env["NORN_AUDIT_SIGNING_KEY"] = WRONG_KEY.decode()
        result = self.invoke()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("signature does not verify", result.stderr)
        self.assertFalse((self.root / "active-generation.json").exists())
        self.assertFalse((self.root / "generations").exists())

    def test_candidate_versioned_predecessor_evidence_is_preserved_and_accepted(self):
        versioned_dir = self.root / f"retarget-{OLD_CANDIDATE}"
        versioned_dir.mkdir(mode=0o700)
        versioned_evidence = versioned_dir / "source-evidence.json"
        versioned_evidence.write_bytes(self.old_evidence.read_bytes())
        versioned_evidence.chmod(0o600)
        journal = json.loads(self.old_journal.read_bytes())
        journal["sourceEvidencePath"] = str(versioned_evidence)
        private(self.old_journal, dump(journal))
        old_bytes = {path: path.read_bytes() for path in (self.old_journal, versioned_evidence, self.old_receipt)}

        result = self.invoke()
        self.assertEqual(result.returncode, 0, result.stderr)
        manifest = json.loads((self.root / "generations" / json.loads(result.stdout)["generationID"] / "generation.json").read_bytes())
        self.assertEqual(manifest["predecessorEvidencePath"], str(versioned_evidence))
        self.assertEqual({path: path.read_bytes() for path in old_bytes}, old_bytes)

    def test_arbitrary_predecessor_evidence_path_is_rejected(self):
        arbitrary = self.root / "other" / "source-evidence.json"
        private(arbitrary, self.old_evidence.read_bytes())
        journal = json.loads(self.old_journal.read_bytes())
        journal["sourceEvidencePath"] = str(arbitrary)
        private(self.old_journal, dump(journal))

        result = self.invoke()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("supported canonical path", result.stderr)
        self.assertFalse((self.root / "active-generation.json").exists())

    def test_source_link_must_be_exact_and_promotion_lock_must_be_available(self):
        self.current.unlink()
        self.current.symlink_to(self.releases / OLD_CANDIDATE)
        wrong_source = self.invoke()
        self.assertNotEqual(wrong_source.returncode, 0)
        self.assertIn("exact prepared source", wrong_source.stderr)
        self.assertFalse((self.root / "active-generation.json").exists())

        lock_path = self.releases / ".promotion.lock"
        fd = os.open(lock_path, os.O_RDWR | os.O_CREAT, 0o600)
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            self.current.unlink()
            self.current.symlink_to(self.releases / SOURCE)
            locked = self.invoke()
            self.assertNotEqual(locked.returncode, 0)
            self.assertIn("another platform promotion", locked.stderr)
            self.assertFalse((self.root / "active-generation.json").exists())
        finally:
            fcntl.flock(fd, fcntl.LOCK_UN)
            os.close(fd)

    def test_incompatible_pointer_and_changed_retry_inputs_fail_closed(self):
        first = self.invoke()
        self.assertEqual(first.returncode, 0, first.stderr)
        shadow = json.loads(self.new_shadow.read_text())
        shadow["shadowPort"] = 19001
        private(self.new_shadow, dump(shadow))
        evidence = json.loads(self.new_evidence.read_text())
        evidence.pop("signature")
        evidence["shadowReceiptSHA256"] = hashlib.sha256(self.new_shadow.read_bytes()).hexdigest()
        private(self.new_evidence, sign(evidence))
        second = self.invoke()
        self.assertNotEqual(second.returncode, 0)
        self.assertIn("active generation pointer is incompatible", second.stderr)


if __name__ == "__main__":
    unittest.main()
