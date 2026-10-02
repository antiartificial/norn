#!/usr/bin/env python3

import json
import hashlib
import os
import subprocess
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("platform-release-manifest")
SHA = "1" * 40
BINARIES = (
    "host-runtime",
    "norn",
    "norn-api",
    "norn-effect-runner",
    "norn-host-agent",
    "norn-ingress-observer",
    "norn-ingress-publisher",
    "platform-release-artifact",
    "platform-release-fetch-github",
    "platform-release-manifest",
    "platform-release-verify-github",
    "platform-upgrade",
)
LEGACY_BINARIES = (
    "host-runtime",
    "norn",
    "norn-api",
    "norn-host-agent",
    "platform-release-artifact",
    "platform-release-fetch-github",
    "platform-release-manifest",
    "platform-release-verify-github",
    "platform-upgrade",
)


class PlatformReleaseManifestTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.source = self.root / "source"
        self.release = self.root / "release"
        (self.source / "v2/api").mkdir(parents=True)
        (self.source / "v2/cli").mkdir(parents=True)
        (self.source / "v2/ui").mkdir(parents=True)
        (self.source / "v2/api/go.sum").write_text("api lock\n", encoding="utf-8")
        (self.source / "v2/cli/go.sum").write_text("cli lock\n", encoding="utf-8")
        (self.source / "v2/ui/pnpm-lock.yaml").write_text("lockfileVersion: '9.0'\n", encoding="utf-8")
        self.populate_release(self.release, BINARIES)
        self.create(self.release)

    def populate_release(self, release: Path, binaries: tuple[str, ...]) -> None:
        (release / "bin").mkdir(parents=True)
        (release / "ui/assets").mkdir(parents=True)
        for name in binaries:
            binary = release / "bin" / name
            binary.write_bytes(name.encode("utf-8"))
            binary.chmod(0o755)
        (release / "ui/index.html").write_text("<main>Norn</main>\n", encoding="utf-8")
        (release / "ui/assets/app.js").write_text("export default 1\n", encoding="utf-8")

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def run_helper(self, *arguments: str, expect_success: bool = True) -> subprocess.CompletedProcess[str]:
        result = subprocess.run(
            [str(SCRIPT), *arguments],
            check=False,
            capture_output=True,
            text=True,
        )
        if expect_success and result.returncode != 0:
            self.fail(f"helper failed: {result.stderr}")
        return result

    def create(self, release: Path, expect_success: bool = True) -> subprocess.CompletedProcess[str]:
        (release / "release.env").write_text(
            f"NORN_RELEASE_SHA={SHA}\nNORN_RELEASE_VERSION=v2.18.0\nNORN_UI_DIR=ui\n",
            encoding="utf-8",
        )
        result = self.run_helper(
            "create",
            "--release", str(release),
            "--source", str(self.source),
            "--sha", SHA,
            "--version", "v2.18.0",
            "--created-at", "2026-08-26T20:00:00Z",
            "--os", "darwin",
            "--arch", "arm64",
            "--go-version", "go1.26.1",
            "--node-version", "v24.19.0",
            "--node-required", "v24.19.0",
            "--pnpm-version", "10.32.1",
            "--pnpm-required", "10.32.1",
            "--go-flag=-trimpath",
            "--go-flag=-buildvcs=false",
            "--go-ldflag=-buildid=",
            "--cgo-enabled", "0",
            "--source-date-epoch", "1787792400",
            expect_success=expect_success,
        )
        if not expect_success:
            return result
        self.seal(release)

        return result

    def seal(self, release: Path) -> None:
        for path in sorted(release.rglob("*"), reverse=True):
            path.chmod(0o555 if path.is_dir() or path.parent == release / "bin" else 0o444)
        release.chmod(0o555)

    def legacy_release(self, signed: bool = False) -> Path:
        legacy = self.root / "legacy-release"
        self.populate_release(legacy, BINARIES)
        self.create(legacy)
        for path in sorted(legacy.rglob("*"), reverse=True):
            path.chmod(0o755 if path.is_dir() else 0o644)
        legacy.chmod(0o755)
        for name in set(BINARIES) - set(LEGACY_BINARIES):
            (legacy / "bin" / name).unlink()
        manifest_path = legacy / "release.json"
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        manifest["artifacts"]["binaries"] = {
            path: digest
            for path, digest in manifest["artifacts"]["binaries"].items()
            if path.removeprefix("bin/") in LEGACY_BINARIES
        }
        manifest_path.write_text(
            json.dumps(manifest, indent=2, sort_keys=True) + "\n", encoding="utf-8"
        )
        signature_path = legacy / "release.signature.json"
        signature = json.loads(signature_path.read_text(encoding="utf-8"))
        if signed:
            signatures = legacy / "signatures"
            signatures.mkdir()
            manifest = signatures / "legacy.manifest.json"
            detached = signatures / "legacy.manifest.sig"
            manifest.write_text("{}\n", encoding="utf-8")
            detached.write_bytes(b"detached-signature")
            signature["status"] = "signed"
            signature["artifacts"] = {
                f"signatures/{path.name}": hashlib.sha256(path.read_bytes()).hexdigest()
                for path in (manifest, detached)
            }
        signature["manifestSha256"] = hashlib.sha256(manifest_path.read_bytes()).hexdigest()
        signature_path.write_text(
            json.dumps(signature, indent=2, sort_keys=True) + "\n", encoding="utf-8"
        )
        self.seal(legacy)
        return legacy

    def verify(self, release: Path, expect_success: bool = True) -> subprocess.CompletedProcess[str]:
        return self.run_helper(
            "verify",
            "--release", str(release),
            "--expected-sha", SHA,
            expect_success=expect_success,
        )

    def test_create_records_compatible_identity_toolchain_inputs_and_artifacts(self) -> None:
        result = self.verify(self.release)
        self.assertEqual(result.stdout.strip(), "unsigned")
        manifest = json.loads((self.release / "release.json").read_text(encoding="utf-8"))
        self.assertEqual(manifest["schema"], "norn.platform-release/v1")
        self.assertEqual(manifest["sha"], SHA)
        self.assertEqual(manifest["source"]["sha"], SHA)
        self.assertEqual(manifest["toolchain"]["node"]["required"], "v24.19.0")
        self.assertEqual(manifest["toolchain"]["pnpm"]["required"], "10.32.1")
        self.assertEqual(
            sorted(manifest["inputs"]["lockfiles"]),
            ["v2/api/go.sum", "v2/cli/go.sum", "v2/ui/pnpm-lock.yaml"],
        )
        self.assertEqual(
            sorted(manifest["artifacts"]["binaries"]),
            [f"bin/{name}" for name in BINARIES],
        )
        self.assertEqual(len(manifest["ui"]["treeSha256"]), 64)

    def test_verify_accepts_legacy_binary_layout_for_rollback_compatibility(self) -> None:
        legacy = self.legacy_release(signed=True)

        result = self.verify(legacy)
        self.assertEqual(result.stdout.strip(), "signed")

    def test_create_rejects_legacy_binary_layout(self) -> None:
        legacy = self.root / "new-legacy-layout"
        self.populate_release(legacy, LEGACY_BINARIES)

        result = self.create(legacy, expect_success=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("new release binary set differs", result.stderr)

    def test_verify_rejects_missing_required_binary_from_current_layout(self) -> None:
        binary = self.release / "bin/norn-effect-runner"
        binary.parent.chmod(0o755)
        binary.unlink()
        binary.parent.chmod(0o555)

        result = self.verify(self.release, expect_success=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("binary set differs", result.stderr)

    def test_verify_rejects_extra_binary_from_current_layout(self) -> None:
        binary = self.release / "bin/unexpected-helper"
        binary.parent.chmod(0o755)
        binary.write_text("unexpected\n", encoding="utf-8")
        binary.chmod(0o555)
        binary.parent.chmod(0o555)

        result = self.verify(self.release, expect_success=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("binary set differs", result.stderr)

    def test_verify_rejects_binary_tampering(self) -> None:
        (self.release / "bin/norn-api").chmod(0o755)
        (self.release / "bin/norn-api").write_bytes(b"tampered")
        (self.release / "bin/norn-api").chmod(0o555)
        result = self.verify(self.release, expect_success=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("binary inventory or digest", result.stderr)

    def test_verify_rejects_ui_tampering(self) -> None:
        (self.release / "ui/index.html").chmod(0o644)
        (self.release / "ui/index.html").write_text("changed\n", encoding="utf-8")
        (self.release / "ui/index.html").chmod(0o444)
        result = self.verify(self.release, expect_success=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("UI inventory or digest", result.stderr)

    def test_verify_rejects_signature_state_for_different_manifest(self) -> None:
        signature_path = self.release / "release.signature.json"
        signature_path.chmod(0o644)
        signature = json.loads(signature_path.read_text(encoding="utf-8"))
        signature["manifestSha256"] = "0" * 64
        signature_path.write_text(json.dumps(signature), encoding="utf-8")
        signature_path.chmod(0o444)
        result = self.verify(self.release, expect_success=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("signature state does not match", result.stderr)

    def test_verify_without_expected_sha_requires_sha_named_directory(self) -> None:
        result = self.run_helper(
            "verify", "--release", str(self.release), expect_success=False
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("does not match source SHA", result.stderr)

    def test_verify_rejects_symlinks_and_hardlinks(self) -> None:
        (self.release / "ui").chmod(0o755)
        os.symlink("index.html", self.release / "ui/link.html")
        (self.release / "ui").chmod(0o555)
        result = self.verify(self.release, expect_success=False)
        self.assertIn("contains a symlink", result.stderr)
        (self.release / "ui").chmod(0o755)
        (self.release / "ui/link.html").unlink()

        os.link(self.release / "ui/index.html", self.release / "ui/hardlink.html")
        (self.release / "ui").chmod(0o555)
        result = self.verify(self.release, expect_success=False)
        self.assertIn("hard-linked files", result.stderr)

    def test_verify_rejects_symlink_release_root(self) -> None:
        linked = self.root / "linked-release"
        os.symlink(self.release, linked)
        result = self.run_helper(
            "verify", "--release", str(linked), "--expected-sha", SHA, expect_success=False
        )
        self.assertIn("non-symlink directory", result.stderr)

    def test_compare_accepts_identical_rebuild_and_rejects_toolchain_drift(self) -> None:
        rebuilt = self.root / "rebuilt"
        (rebuilt / "bin").mkdir(parents=True)
        (rebuilt / "ui/assets").mkdir(parents=True)
        for name in BINARIES:
            binary = rebuilt / "bin" / name
            binary.write_bytes(name.encode("utf-8"))
            binary.chmod(0o755)
        (rebuilt / "ui/index.html").write_text("<main>Norn</main>\n", encoding="utf-8")
        (rebuilt / "ui/assets/app.js").write_text("export default 1\n", encoding="utf-8")
        self.create(rebuilt)
        self.run_helper("compare", "--retained", str(self.release), "--rebuilt", str(rebuilt))

        manifest_path = rebuilt / "release.json"
        manifest_path.chmod(0o644)
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        manifest["toolchain"]["go"]["version"] = "go1.26.2"
        manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
        result = self.run_helper(
            "compare", "--retained", str(self.release), "--rebuilt", str(rebuilt), expect_success=False
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("rebuild manifest differs", result.stderr)

    def test_atomic_install_never_replaces_existing_destination(self) -> None:
        staging = self.root / "install-stage"
        destination = self.root / "installed"
        staging.mkdir()
        (staging / "marker").write_text("first", encoding="utf-8")
        self.run_helper(
            "install", "--staging", str(staging), "--destination", str(destination)
        )
        self.assertEqual((destination / "marker").read_text(encoding="utf-8"), "first")

        second = self.root / "second-stage"
        second.mkdir()
        (second / "marker").write_text("second", encoding="utf-8")
        result = self.run_helper(
            "install", "--staging", str(second), "--destination", str(destination), expect_success=False
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual((destination / "marker").read_text(encoding="utf-8"), "first")


if __name__ == "__main__":
    unittest.main()
