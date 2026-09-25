#!/usr/bin/env python3
"""Disposable fixtures for the read-only legacy-baseline doctor."""

import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
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
        for setting in ("user.name", "user.email"):
            configured = subprocess.check_output(["git", "config", "--get", setting], text=True).strip()
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
        self.assertIn("signed", doctor.check_signed_release(args))
        verifier.write_text("#!/bin/sh\nprintf 'unsigned\\n'\n", encoding="utf-8")
        with self.assertRaises(doctor.CheckFailure):
            doctor.check_signed_release(args)
        with mock.patch.object(doctor, "command", side_effect=[b"    pid = 12345\n", b"12345\n"]):
            self.assertIn("solely", doctor.check_listener(args))
        with mock.patch.object(doctor, "command", side_effect=[b"    pid = 12345\n", b"98765\n"]):
            with self.assertRaises(doctor.CheckFailure):
                doctor.check_listener(args)
        installed.write_bytes(b"other")
        with self.assertRaises(doctor.CheckFailure):
            doctor.check_legacy_release(args)

    def test_drain_requires_fail_mode_before_http(self):
        args = types.SimpleNamespace(api_base="http://127.0.0.1:8800")
        with mock.patch.dict(os.environ, {"NORN_DRAIN_MODE": "force", "NORN_API_TOKEN": "secret"}):
            with self.assertRaises(doctor.CheckFailure):
                doctor.check_drain(args)
        with self.assertRaises(doctor.CheckFailure):
            doctor.direct_port("https://elsewhere.example:8800")


if __name__ == "__main__":
    unittest.main()
