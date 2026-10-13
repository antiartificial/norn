#!/usr/bin/env python3
"""Hermetic tests for recovered-forward generation selection."""

from __future__ import annotations

import hashlib
import hmac
import importlib.machinery
import importlib.util
import json
import os
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("mini-recovered-forward-generation-resolve")
LOADER = importlib.machinery.SourceFileLoader("mini_recovered_forward_generation_resolve", str(SCRIPT))
SPEC = importlib.util.spec_from_loader(LOADER.name, LOADER)
assert SPEC and SPEC.loader
RESOLVER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(RESOLVER)


class GenerationResolverTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name).resolve() / "recovered-forward"
        self.root.mkdir(mode=0o700)
        self.key = b"hermetic-generation-resolver-key-32bytes"
        self.generation_id = "mini-3da50-to-87dd-001"
        self.transition_id = "mini-v3-forward-20261013"
        self.source = "3" * 40
        self.candidate = "8" * 40

    def tearDown(self) -> None:
        self.temp.cleanup()

    def sign(self, value: dict) -> bytes:
        unsigned = dict(value)
        schema = unsigned["schema"]
        signature = "hmac-sha256:" + hmac.new(
            self.key, schema.encode() + b"\0" + RESOLVER.canonical(unsigned), hashlib.sha256,
        ).hexdigest()
        return RESOLVER.canonical({**unsigned, "signature": signature}) + b"\n"

    def predecessor_fields(self) -> dict:
        journal = self.root / "transition.json"
        evidence = self.root / "transition.json.source-evidence.json"
        receipt = self.root / "preservation-receipt.json"
        self.write_private(journal, b"legacy journal fixture")
        self.write_private(evidence, b"legacy evidence fixture")
        self.write_private(receipt, b"legacy receipt fixture")
        return {
            "predecessorState": "candidate-promoted",
            "predecessorTransitionID": "mini-v3-old-transition",
            "predecessorSourceSHA": "1" * 40,
            "predecessorCandidateSHA": "2" * 40,
            "predecessorJournalPath": str(journal),
            "predecessorJournalSHA256": hashlib.sha256(journal.read_bytes()).hexdigest(),
            "predecessorEvidencePath": str(evidence),
            "predecessorEvidenceSHA256": hashlib.sha256(evidence.read_bytes()).hexdigest(),
            "predecessorReceiptPath": str(receipt),
            "predecessorReceiptSHA256": hashlib.sha256(receipt.read_bytes()).hexdigest(),
        }

    def write_private(self, path: Path, raw: bytes, mode: int = 0o600) -> None:
        path.write_bytes(raw)
        path.chmod(mode)

    def install_generation(self) -> tuple[Path, Path, bytes, bytes]:
        generations = self.root / "generations"
        generations.mkdir(mode=0o700)
        generation = generations / self.generation_id
        generation.mkdir(mode=0o700)
        evidence = generation / "transition.json.source-evidence.json"
        proof = generation / "backup-proof.json"
        artifact = self.root / "generation-backup-artifact.bin"
        shadow = generation / "shadow-receipt.json"
        self.write_private(evidence, b"generation evidence")
        self.write_private(proof, b"generation proof")
        self.write_private(artifact, b"generation backup artifact")
        self.write_private(shadow, b"generation shadow")
        manifest_path = generation / "generation.json"
        manifest = {
            "schema": RESOLVER.MANIFEST_SCHEMA,
            "generationID": self.generation_id,
            "generationPath": str(generation),
            "transitionID": self.transition_id,
            "sourceSHA": self.source,
            "candidateSHA": self.candidate,
            "journalPath": str(generation / "transition.json"),
            "preservationReceiptPath": str(generation / "preservation-receipt.json"),
            "createdAt": "2026-10-13T12:00:00Z",
            "sourceEvidencePath": str(evidence),
            "sourceEvidenceSHA256": hashlib.sha256(evidence.read_bytes()).hexdigest(),
            "backupProofPath": str(proof),
            "backupProofSHA256": hashlib.sha256(proof.read_bytes()).hexdigest(),
            "backupArtifactPath": str(artifact),
            "backupArtifactSHA256": hashlib.sha256(artifact.read_bytes()).hexdigest(),
            "backupArtifactBytes": len(artifact.read_bytes()),
            "shadowReceiptPath": str(shadow),
            "shadowReceiptSHA256": hashlib.sha256(shadow.read_bytes()).hexdigest(),
            **self.predecessor_fields(),
        }
        manifest_raw = self.sign(manifest)
        self.write_private(manifest_path, manifest_raw)
        journal = {
            "schema": "norn.recovered-forward-transition/v1",
            "state": "prepared",
            "transitionID": self.transition_id,
            "sourceSHA": self.source,
            "candidateReleaseSHA": self.candidate,
            "sourceEvidencePath": str(evidence),
            "sourceEvidenceSHA256": hashlib.sha256(evidence.read_bytes()).hexdigest(),
            "backupProofPath": str(proof),
            "backupArtifactPath": str(artifact),
            "shadowReceiptPath": str(shadow),
            "preservationReceiptPath": str(generation / "preservation-receipt.json"),
            "preservationReceiptSHA256": "",
        }
        self.write_private(generation / "transition.json", RESOLVER.canonical(journal) + b"\n")
        pointer = {
            "schema": RESOLVER.POINTER_SCHEMA,
            "generationID": self.generation_id,
            "generationPath": str(generation),
            "generationManifestPath": str(manifest_path),
            "generationManifestSHA256": hashlib.sha256(manifest_raw).hexdigest(),
            "transitionID": self.transition_id,
            "sourceSHA": self.source,
            "candidateSHA": self.candidate,
            "activatedAt": "2026-10-13T12:01:00Z",
        }
        pointer_raw = self.sign(pointer)
        self.write_private(self.root / "active-generation.json", pointer_raw)
        return generation, manifest_path, manifest_raw, pointer_raw

    def test_absent_pointer_selects_legacy_fixed_paths_without_key(self) -> None:
        result = RESOLVER.resolve(self.root, b"")
        self.assertEqual(result["selection"], "legacy-fixed")
        self.assertEqual(result["journalPath"], str(self.root / "transition.json"))
        self.assertEqual(result["preservationReceiptPath"], str(self.root / "preservation-receipt.json"))
        self.assertIsNone(result["generationID"])

    def test_valid_pointer_selects_exact_generation(self) -> None:
        generation, manifest_path, manifest_raw, _ = self.install_generation()
        result = RESOLVER.resolve(self.root, self.key)
        self.assertEqual(result["selection"], "generation")
        self.assertEqual(result["generationID"], self.generation_id)
        self.assertEqual(result["generationPath"], str(generation))
        self.assertEqual(result["generationManifestPath"], str(manifest_path))
        self.assertEqual(result["generationManifestSHA256"], hashlib.sha256(manifest_raw).hexdigest())
        self.assertEqual(result["journalPath"], str(generation / "transition.json"))
        self.assertEqual(result["preservationReceiptPath"], str(generation / "preservation-receipt.json"))
        self.assertEqual(result["transitionID"], self.transition_id)
        self.assertEqual(result["sourceSHA"], self.source)
        self.assertEqual(result["candidateSHA"], self.candidate)

    def test_historical_generation_key_with_distinct_current_authorization_key(self) -> None:
        self.install_generation()
        current = b"different-current-audit-signing-key-32bytes"
        selected = RESOLVER.resolve_with_audit_keys(self.root, current, self.key)
        self.assertEqual(selected["candidateSHA"], self.candidate)
        with self.assertRaisesRegex(ValueError, "HMAC"):
            RESOLVER.resolve_with_audit_keys(self.root, current, b"wrong-historical-key-32bytes-at-least")

    def test_fenced_phase_requires_explicit_repair_selection(self) -> None:
        generation, _, _, _ = self.install_generation()
        journal_path = generation / "transition.json"
        journal = json.loads(journal_path.read_bytes())
        journal["state"] = "host-agent-fencing"
        self.write_private(journal_path, RESOLVER.canonical(journal) + b"\n")
        with self.assertRaisesRegex(ValueError, "state is unsupported"):
            RESOLVER.resolve(self.root, self.key)
        selected = RESOLVER.resolve(self.root, self.key, allow_repair_phase=True)
        self.assertEqual(selected["candidateSHA"], self.candidate)
        journal["state"] = "schema-migrated"
        self.write_private(journal_path, RESOLVER.canonical(journal) + b"\n")
        with self.assertRaisesRegex(ValueError, "state is unsupported"):
            RESOLVER.resolve(self.root, self.key)
        self.assertEqual(RESOLVER.resolve(self.root, self.key, allow_repair_phase=True)["candidateSHA"], self.candidate)
        journal["state"] = "candidate-promoted"
        self.write_private(journal_path, RESOLVER.canonical(journal) + b"\n")
        self.assertEqual(RESOLVER.resolve(self.root, self.key)["candidateSHA"], self.candidate)
        journal["state"] = "unknown-phase"
        self.write_private(journal_path, RESOLVER.canonical(journal) + b"\n")
        with self.assertRaisesRegex(ValueError, "state is unsupported"):
            RESOLVER.resolve(self.root, self.key, allow_repair_phase=True)

    def test_candidate_versioned_predecessor_evidence_resolves(self) -> None:
        generation, manifest_path, _, _ = self.install_generation()
        retarget_dir = self.root / f"retarget-{'2' * 40}"
        retarget_dir.mkdir(mode=0o700)
        evidence = retarget_dir / "source-evidence.json"
        self.write_private(evidence, b"legacy evidence fixture")
        manifest = json.loads(manifest_path.read_bytes())
        manifest.pop("signature")
        manifest["predecessorEvidencePath"] = str(evidence)
        manifest["predecessorEvidenceSHA256"] = hashlib.sha256(evidence.read_bytes()).hexdigest()
        raw = self.sign(manifest)
        self.write_private(manifest_path, raw)
        pointer = self.root / "active-generation.json"
        value = json.loads(pointer.read_bytes())
        value.pop("signature")
        value["generationManifestSHA256"] = hashlib.sha256(raw).hexdigest()
        self.write_private(pointer, self.sign(value))
        result = RESOLVER.resolve(self.root, self.key)
        self.assertEqual(result["selection"], "generation")

    def test_arbitrary_predecessor_evidence_path_is_rejected(self) -> None:
        _, manifest_path, _, _ = self.install_generation()
        manifest = json.loads(manifest_path.read_bytes())
        manifest.pop("signature")
        arbitrary = self.root / "other" / "source-evidence.json"
        arbitrary.parent.mkdir(mode=0o700)
        self.write_private(arbitrary, b"legacy evidence fixture")
        manifest["predecessorEvidencePath"] = str(arbitrary)
        manifest["predecessorEvidenceSHA256"] = hashlib.sha256(arbitrary.read_bytes()).hexdigest()
        raw = self.sign(manifest)
        self.write_private(manifest_path, raw)
        pointer = self.root / "active-generation.json"
        value = json.loads(pointer.read_bytes())
        value.pop("signature")
        value["generationManifestSHA256"] = hashlib.sha256(raw).hexdigest()
        self.write_private(pointer, self.sign(value))
        with self.assertRaisesRegex(ValueError, "supported canonical path"):
            RESOLVER.resolve(self.root, self.key)

    def test_mutated_journal_identity_does_not_follow_valid_signed_pointer(self) -> None:
        generation, _, _, _ = self.install_generation()
        journal_path = generation / "transition.json"
        journal = json.loads(journal_path.read_bytes())
        journal["candidateReleaseSHA"] = "9" * 40
        self.write_private(journal_path, RESOLVER.canonical(journal) + b"\n")
        with self.assertRaisesRegex(ValueError, "journal does not match"):
            RESOLVER.resolve(self.root, self.key)

    def test_pointer_never_falls_back_when_unsafe_or_invalid(self) -> None:
        cases = []

        def bad_mode() -> None:
            self.install_generation()
            (self.root / "active-generation.json").chmod(0o644)

        cases.append(("mode", bad_mode, "mode-0600"))

        def bad_signature() -> None:
            self.install_generation()
            path = self.root / "active-generation.json"
            value = json.loads(path.read_bytes())
            value["candidateSHA"] = "9" * 40
            self.write_private(path, RESOLVER.canonical(value) + b"\n")

        cases.append(("signature", bad_signature, "signature"))

        def missing_manifest() -> None:
            _, manifest, _, _ = self.install_generation()
            manifest.unlink()

        cases.append(("missing-manifest", missing_manifest, "manifest"))

        def digest_mismatch() -> None:
            _, manifest, _, _ = self.install_generation()
            value = json.loads(manifest.read_bytes())
            value["createdAt"] = "2026-10-13T12:02:00Z"
            self.write_private(manifest, self.sign({k: v for k, v in value.items() if k != "signature"}))

        cases.append(("manifest-digest", digest_mismatch, "digest"))

        def pointer_symlink() -> None:
            _, _, _, pointer_raw = self.install_generation()
            pointer = self.root / "active-generation.json"
            pointer.unlink()
            target = self.root / "pointer-target.json"
            self.write_private(target, pointer_raw)
            pointer.symlink_to(target)

        cases.append(("symlink", pointer_symlink, "open"))

        for name, mutate, message in cases:
            with self.subTest(name=name):
                self.tearDown()
                self.setUp()
                mutate()
                with self.assertRaisesRegex(ValueError, message):
                    RESOLVER.resolve(self.root, self.key)

    def test_rejects_noncanonical_generation_or_manifest_bindings(self) -> None:
        generation, manifest_path, _, _ = self.install_generation()
        pointer_path = self.root / "active-generation.json"
        pointer = json.loads(pointer_path.read_bytes())

        for field, replacement in (
            ("generationPath", str(self.root)),
            ("generationManifestPath", str(generation / "other.json")),
            ("generationID", "../escape"),
        ):
            with self.subTest(field=field):
                changed = {k: v for k, v in pointer.items() if k != "signature"}
                changed[field] = replacement
                self.write_private(pointer_path, self.sign(changed))
                with self.assertRaises(ValueError):
                    RESOLVER.resolve(self.root, self.key)

        # Restore the pointer, then prove a signed manifest with a changed path
        # is still rejected even when the pointer digest is updated correctly.
        self.install_clean_pointer(generation, manifest_path)
        manifest = json.loads(manifest_path.read_bytes())
        unsigned_manifest = {k: v for k, v in manifest.items() if k != "signature"}
        unsigned_manifest["journalPath"] = str(self.root / "transition.json")
        manifest_raw = self.sign(unsigned_manifest)
        self.write_private(manifest_path, manifest_raw)
        pointer = json.loads(pointer_path.read_bytes())
        unsigned_pointer = {k: v for k, v in pointer.items() if k != "signature"}
        unsigned_pointer["generationManifestSHA256"] = hashlib.sha256(manifest_raw).hexdigest()
        self.write_private(pointer_path, self.sign(unsigned_pointer))
        with self.assertRaisesRegex(ValueError, "journalPath"):
            RESOLVER.resolve(self.root, self.key)

    def test_predecessor_closure_digest_is_enforced(self) -> None:
        self.install_generation()
        predecessor = self.root / "transition.json"
        predecessor.write_bytes(b"changed journal")
        predecessor.chmod(0o600)
        with self.assertRaisesRegex(ValueError, "predecessor digest changed"):
            RESOLVER.resolve(self.root, self.key)

    def test_external_backup_artifact_size_is_manifest_bound(self) -> None:
        self.install_generation()
        artifact = self.root / "generation-backup-artifact.bin"
        self.write_private(artifact, b"changed size")
        with self.assertRaisesRegex(ValueError, "byte count"):
            RESOLVER.resolve(self.root, self.key)

    def install_clean_pointer(self, generation: Path, manifest_path: Path) -> None:
        manifest_raw = manifest_path.read_bytes()
        pointer = {
            "schema": RESOLVER.POINTER_SCHEMA,
            "generationID": self.generation_id,
            "generationPath": str(generation),
            "generationManifestPath": str(manifest_path),
            "generationManifestSHA256": hashlib.sha256(manifest_raw).hexdigest(),
            "transitionID": self.transition_id,
            "sourceSHA": self.source,
            "candidateSHA": self.candidate,
            "activatedAt": "2026-10-13T12:01:00Z",
        }
        self.write_private(self.root / "active-generation.json", self.sign(pointer))

    def test_rejects_wrong_manifest_mode_signature_fields_and_tuple(self) -> None:
        generation, manifest_path, _, _ = self.install_generation()
        pointer_path = self.root / "active-generation.json"

        manifest_path.chmod(0o644)
        with self.assertRaisesRegex(ValueError, "mode-0600"):
            RESOLVER.resolve(self.root, self.key)
        manifest_path.chmod(0o600)

        manifest = json.loads(manifest_path.read_bytes())
        unsigned = {k: v for k, v in manifest.items() if k != "signature"}
        unsigned["sourceSHA"] = "4" * 40
        raw = self.sign(unsigned)
        self.write_private(manifest_path, raw)
        pointer = json.loads(pointer_path.read_bytes())
        pointer_unsigned = {k: v for k, v in pointer.items() if k != "signature"}
        pointer_unsigned["generationManifestSHA256"] = hashlib.sha256(raw).hexdigest()
        self.write_private(pointer_path, self.sign(pointer_unsigned))
        with self.assertRaisesRegex(ValueError, "sourceSHA"):
            RESOLVER.resolve(self.root, self.key)

        unsigned["unexpected"] = True
        raw = self.sign(unsigned)
        self.write_private(manifest_path, raw)
        pointer_unsigned["generationManifestSHA256"] = hashlib.sha256(raw).hexdigest()
        self.write_private(pointer_path, self.sign(pointer_unsigned))
        with self.assertRaisesRegex(ValueError, "fields"):
            RESOLVER.resolve(self.root, self.key)

    def test_present_pointer_requires_audit_key(self) -> None:
        self.install_generation()
        with self.assertRaisesRegex(ValueError, "at least 32 bytes"):
            RESOLVER.resolve(self.root, b"")


if __name__ == "__main__":
    unittest.main()
