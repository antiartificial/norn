#!/usr/bin/env python3
"""Disposable fixtures for the read-only legacy-baseline doctor."""

import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import time
import types
import unittest
from unittest import mock


SCRIPT = Path(__file__).with_name("legacy-baseline-doctor")
loader = importlib.machinery.SourceFileLoader("legacy_baseline_doctor", str(SCRIPT))
spec = importlib.util.spec_from_loader(loader.name, loader)
doctor = importlib.util.module_from_spec(spec)
loader.exec_module(doctor)


class DoctorFixtureTest(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory(prefix="norn-legacy-doctor-")
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        self.sops = self.root / "api.env.enc.json"
        self.sops.write_text("encrypted fixture", encoding="utf-8")
        self.launcher = self.root / "launcher.py"
        self.launcher.write_text(
            "import os,subprocess\n"
            f"env_file = os.path.expanduser({str(self.sops)!r})\n"
            "raw = subprocess.check_output(['sops','--decrypt',env_file])\n"
            "os.execve('/test/api', [], {})\n", encoding="utf-8"
        )
        self.url = "postgres://test@localhost/control"
        self.key = "fixture-audit-signing-key-longer-than-32"
        self.args = types.SimpleNamespace(launcher=self.launcher, sops_env=self.sops, sops="sops")

    def test_binding_requires_exact_launcher_and_maintenance_values_without_echo(self):
        runtime = {"NORN_DATABASE_URL": self.url, "NORN_AUDIT_SIGNING_KEY": self.key}
        with mock.patch.object(doctor, "command", return_value=json.dumps(runtime).encode()), mock.patch.dict(os.environ, runtime):
            self.assertIn("equal", doctor.check_binding(self.args))
        with mock.patch.object(doctor, "command", return_value=json.dumps(runtime).encode()), mock.patch.dict(os.environ, {**runtime, "NORN_AUDIT_SIGNING_KEY": "different-secret-material-over-32-bytes"}):
            with self.assertRaises(doctor.CheckFailure) as raised:
                doctor.check_binding(self.args)
            self.assertNotIn("different-secret", str(raised.exception))
            self.assertNotIn(self.key, str(raised.exception))

    def test_reviewed_script_and_release_are_exact_source_bound(self):
        repo = self.root / "repo"
        repo.mkdir()
        subprocess.run(["git", "-C", str(repo), "init", "-q"], check=True)
        for setting, field in (("user.name", "%an"), ("user.email", "%ae")):
            configured = subprocess.check_output(["git", "-C", str(SCRIPT.parent), "log", "-1", f"--format={field}"], text=True).strip()
            subprocess.run(["git", "-C", str(repo), "config", setting, configured], check=True)
        source_script = repo / "v2/scripts/platform-upgrade"
        source_script.parent.mkdir(parents=True)
        source_script.write_text("#!/bin/sh\n# legacy-baseline fixture\n", encoding="utf-8")
        source_script.chmod(0o755)
        subprocess.run(["git", "-C", str(repo), "add", "."], check=True)
        subprocess.run(["git", "-C", str(repo), "commit", "-qm", "fixture"], check=True)
        sha = subprocess.check_output(["git", "-C", str(repo), "rev-parse", "HEAD"], text=True).strip()
        args = types.SimpleNamespace(repo=repo, candidate_sha=sha, reviewed_script=source_script, releases=self.root / "releases")
        self.assertIn("exact candidate", doctor.check_reviewed_script(args))
        release = args.releases / sha
        (release / "bin").mkdir(parents=True)
        (release / "release.env").write_text(f"NORN_RELEASE_SHA={sha}\n", encoding="utf-8")
        (release / "bin/platform-upgrade").write_bytes(source_script.read_bytes())
        binary = release / "bin/norn-api"
        binary.write_text("#!/bin/sh\nprintf '%s\\n' '{\"name\":\"norn.startup/v2\",\"schemaModes\":[\"migrate-only\"],\"startupModes\":[\"passive\"]}'\n", encoding="utf-8")
        binary.chmod(0o755)
        self.assertIn("startup contract", doctor.check_candidate_release(args))
        (release / "bin/platform-upgrade").write_text("wrong", encoding="utf-8")
        with self.assertRaises(doctor.CheckFailure):
            doctor.check_candidate_release(args)
        (release / "bin/platform-upgrade").write_bytes(source_script.read_bytes())
        binary.write_text("#!/bin/sh\nprintf 'null\\n'\n", encoding="utf-8")
        with self.assertRaises(doctor.CheckFailure):
            doctor.check_candidate_release(args)

    def test_legacy_release_and_listener_owner_require_exact_match(self):
        sha = "a" * 40
        releases = self.root / "releases"
        release = releases / sha
        (release / "bin").mkdir(parents=True)
        (release / "release.env").write_text(f"NORN_RELEASE_SHA={sha}\n", encoding="utf-8")
        (release / "bin/norn-api").write_bytes(b"legacy-binary")
        current = self.root / "current"
        current.symlink_to(release)
        installed = self.root / "norn-api"
        installed.write_bytes(b"legacy-binary")
        verifier = release / "bin/platform-release-manifest"
        verifier.write_text("#!/bin/sh\nprintf 'signed\\n'\n", encoding="utf-8")
        verifier.chmod(0o755)
        args = types.SimpleNamespace(releases=releases, legacy_release=sha, candidate_sha="b" * 40, current_link=current, legacy_api=installed, api_base="http://127.0.0.1:8800", launch_label="com.norn.api", release_verifier=None)
        self.assertIn("match", doctor.check_legacy_release(args))
        with mock.patch.object(doctor, "command", side_effect=[b"    pid = 12345\n", b"12345\n"]):
            self.assertIn("solely", doctor.check_listener(args))
        with mock.patch.object(doctor, "command", side_effect=[b"    pid = 12345\n", b"98765\n"]):
            with self.assertRaises(doctor.CheckFailure):
                doctor.check_listener(args)
        installed.write_bytes(b"other")
        with self.assertRaises(doctor.CheckFailure):
            doctor.check_legacy_release(args)

    def test_signed_release_requires_reviewed_crypto_verifier_and_key(self):
        repo = self.root / "repo"
        source = repo / "v2/scripts/platform-release-artifact"
        source.parent.mkdir(parents=True)
        source.write_text("#!/bin/sh\n[ \"${NORN_FIXTURE_SIGNATURE_VALID:-yes}\" = yes ]\n", encoding="utf-8")
        source.chmod(0o755)
        subprocess.run(["git", "-C", str(repo), "init", "-q"], check=True)
        for setting, field in (("user.name", "%an"), ("user.email", "%ae")):
            configured = subprocess.check_output(["git", "-C", str(SCRIPT.parent), "log", "-1", f"--format={field}"], text=True).strip()
            subprocess.run(["git", "-C", str(repo), "config", setting, configured], check=True)
        subprocess.run(["git", "-C", str(repo), "add", "."], check=True)
        subprocess.run(["git", "-C", str(repo), "commit", "-qm", "fixture"], check=True)
        sha = subprocess.check_output(["git", "-C", str(repo), "rev-parse", "HEAD"], text=True).strip()
        manifest = self.root / "manifest"
        manifest.write_text("#!/bin/sh\nprintf 'signed\\n'\n", encoding="utf-8")
        manifest.chmod(0o755)
        key = self.root / "release.pub"
        key.write_text("fixture", encoding="utf-8")
        args = types.SimpleNamespace(repo=repo, candidate_sha=sha, legacy_release="a" * 40,
                                     releases=self.root / "releases", current_link=self.root / "current",
                                     release_verifier=manifest, artifact_verifier=source, public_key=key)
        self.assertIn("Ed25519", doctor.check_signed_release(args))
        with mock.patch.dict(os.environ, {"NORN_FIXTURE_SIGNATURE_VALID": "no"}):
            with self.assertRaises(doctor.CheckFailure):
                doctor.check_signed_release(args)
        source.write_text("#!/bin/sh\nexit 1\n", encoding="utf-8")
        with self.assertRaisesRegex(doctor.CheckFailure, "differs from candidate source"):
            doctor.check_signed_release(args)
        source.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
        key.unlink()
        with self.assertRaisesRegex(doctor.CheckFailure, "public key is absent"):
            doctor.check_signed_release(args)

    def test_signed_release_verifies_two_real_imports_and_rejects_wrong_key(self):
        artifact = SCRIPT.with_name("platform-release-artifact")
        manifest = SCRIPT.with_name("platform-release-manifest")
        openssl = next((path for path in (
            "/opt/homebrew/opt/openssl@3/bin/openssl", "/usr/local/opt/openssl@3/bin/openssl",
            shutil.which("openssl")) if path and Path(path).is_file()), None)
        if not openssl:
            self.skipTest("OpenSSL is required")

        def run(*argv, env=None):
            return subprocess.run([str(item) for item in argv], env=env, check=True,
                                  capture_output=True, text=True)

        repo = self.root / "repo"
        reviewed = repo / "v2/scripts/platform-release-artifact"
        reviewed.parent.mkdir(parents=True)
        shutil.copy2(artifact, reviewed)
        run("git", "-C", repo, "init", "-q")
        for setting, field in (("user.name", "%an"), ("user.email", "%ae")):
            identity = run("git", "-C", SCRIPT.parent, "log", "-1", f"--format={field}").stdout.strip()
            run("git", "-C", repo, "config", setting, identity)
        run("git", "-C", repo, "add", ".")
        run("git", "-C", repo, "commit", "-qm", "fixture")
        candidate_sha = run("git", "-C", repo, "rev-parse", "HEAD").stdout.strip()
        legacy_sha = "a" * 40

        private_key = self.root / "private.pem"
        public_key = self.root / "public.pem"
        wrong_key = self.root / "wrong.pem"
        run(openssl, "genpkey", "-algorithm", "ED25519", "-out", private_key)
        run(openssl, "pkey", "-in", private_key, "-pubout", "-out", public_key)
        other_private = self.root / "other-private.pem"
        run(openssl, "genpkey", "-algorithm", "ED25519", "-out", other_private)
        run(openssl, "pkey", "-in", other_private, "-pubout", "-out", wrong_key)
        source = self.root / "source"
        for relative in ("v2/api/go.sum", "v2/cli/go.sum", "v2/ui/pnpm-lock.yaml"):
            target = source / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text("fixture\n", encoding="utf-8")
        releases = self.root / "releases"
        binaries = ("host-runtime", "norn", "norn-api", "norn-effect-runner", "norn-host-agent",
                    "norn-ingress-observer", "norn-ingress-publisher", "platform-release-artifact", "platform-release-fetch-github",
                    "platform-release-manifest", "platform-release-verify-github", "platform-upgrade")
        for sha in (legacy_sha, candidate_sha):
            release = self.root / f"unsigned-{sha}"
            (release / "bin").mkdir(parents=True)
            (release / "ui").mkdir()
            (release / "ui/index.html").write_text("fixture\n", encoding="utf-8")
            for name in binaries:
                binary = release / "bin" / name
                binary.write_text(f"{name}\n", encoding="utf-8")
                binary.chmod(0o755)
            (release / "release.env").write_text(
                f"NORN_RELEASE_SHA={sha}\nNORN_RELEASE_VERSION=v2-test\n"
                "NORN_RELEASE_CREATED_AT=2026-08-26T20:00:00Z\nNORN_UI_DIR=ui\n", encoding="utf-8")
            run(manifest, "create", "--release", release, "--source", source,
                "--sha", sha, "--version", "v2-test", "--created-at", "2026-08-26T20:00:00Z",
                "--os", "linux", "--arch", "amd64", "--go-version", "go1.test",
                "--node-version", "v24.19.0", "--node-required", "v24.19.0",
                "--pnpm-version", "10.32.1", "--pnpm-required", "10.32.1",
                "--go-flag=-trimpath", "--go-ldflag=-buildid=", "--cgo-enabled", "0",
                "--source-date-epoch", "1787792400")
            bundle = self.root / f"bundle-{sha}"
            run(artifact, "package", "--release-dir", release, "--output-dir", bundle,
                "--commit", sha, "--os", "linux", "--arch", "amd64",
                "--repository", "antiartificial/norn", "--source-date-epoch", "1787792400")
            run(artifact, "sign", "--bundle-dir", bundle,
                env=os.environ | {"NORN_RELEASE_SIGNING_KEY": private_key.read_text()})
            run(artifact, "import", "--bundle-dir", bundle, "--releases-dir", releases,
                "--public-key", public_key)

        current = self.root / "current"
        current.symlink_to(releases / legacy_sha)
        args = types.SimpleNamespace(repo=repo, candidate_sha=candidate_sha, legacy_release=legacy_sha,
                                     releases=releases, current_link=current, release_verifier=manifest,
                                     artifact_verifier=reviewed, public_key=public_key)
        self.assertIn("Ed25519", doctor.check_signed_release(args))
        args.public_key = wrong_key
        with self.assertRaises(doctor.CheckFailure):
            doctor.check_signed_release(args)
        args.public_key = public_key
        candidate_binary = releases / candidate_sha / "bin/norn-api"
        candidate_binary.chmod(0o755)
        candidate_binary.write_text("tampered\n", encoding="utf-8")
        with self.assertRaises(doctor.CheckFailure):
            doctor.check_signed_release(args)

    def test_runtime_process_must_start_after_binding_change(self):
        installed = self.root / "norn-api"
        installed.write_bytes(b"legacy-binary")
        args = types.SimpleNamespace(launcher=self.launcher, sops_env=self.sops, legacy_api=installed, launch_label="com.norn.api")
        state = b"    pid = 12345\n"
        old_start = time.strftime("%a %b %d %H:%M:%S %Y", time.localtime(time.time() - 60)).encode()
        new_start = time.strftime("%a %b %d %H:%M:%S %Y", time.localtime(time.time() + 60)).encode()
        mapped = f"i{installed.stat().st_ino}\nn{installed.resolve()}\n".encode()
        with mock.patch.object(doctor, "command", side_effect=[state, old_start]):
            with self.assertRaisesRegex(doctor.CheckFailure, "predates launcher or SOPS"):
                doctor.check_runtime_process_freshness(args)
        with mock.patch.object(doctor, "command", side_effect=[state, new_start, mapped]):
            self.assertIn("maps installed executable", doctor.check_runtime_process_freshness(args))
        with mock.patch.object(doctor, "command", side_effect=[state, new_start, b"i1\nn/elsewhere/api\n"]):
            with self.assertRaisesRegex(doctor.CheckFailure, "does not map"):
                doctor.check_runtime_process_freshness(args)

    def test_drain_requires_fail_mode_before_http(self):
        args = types.SimpleNamespace(api_base="http://127.0.0.1:8800")
        with mock.patch.dict(os.environ, {"NORN_DRAIN_MODE": "force", "NORN_API_TOKEN": "secret"}):
            with self.assertRaises(doctor.CheckFailure):
                doctor.check_drain(args)
        with self.assertRaises(doctor.CheckFailure):
            doctor.direct_port("https://elsewhere.example:8800")


if __name__ == "__main__":
    unittest.main()
