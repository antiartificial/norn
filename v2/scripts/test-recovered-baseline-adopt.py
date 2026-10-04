#!/usr/bin/env python3
"""Exercise the recovered-baseline gate through the real platform script."""

import hashlib
import json
import os
import shutil
import stat
import subprocess
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("platform-upgrade")
LEGACY = "a5da8ef15d12e9eca7561e90b90d96f6dc652a21"
CANDIDATE = "e7301f779041cd22f1872090323d3b8ed5c4611b"
ADOPTER = "a" * 40
VERSION = "v2.20.0-platform-62-ge7301f77"


def write(path: Path, content: str | bytes, mode: int = 0o600) -> Path:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(content.encode() if isinstance(content, str) else content)
    path.chmod(mode)
    return path


def write_json(path: Path, value: dict) -> Path:
    return write(path, json.dumps(value, sort_keys=True) + "\n")


class RecoveredBaselineAdoptionTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.home = self.root / "home"
        self.home.mkdir(mode=0o700)
        (self.home / ".config" / "norn").mkdir(parents=True, mode=0o700)
        self.releases = self.root / "releases"
        self.current = self.releases / CANDIDATE
        self.adopter = self.releases / ADOPTER
        for release, sha in ((self.current, CANDIDATE), (self.adopter, ADOPTER)):
            (release / "bin").mkdir(parents=True)
            write(release / "release.env", f"NORN_RELEASE_SHA={sha}\nNORN_RELEASE_VERSION={VERSION}\n", 0o644)
            write(release / "release.json", "{}\n", 0o644)
        self.current_link = self.root / "current"
        self.current_link.symlink_to(self.current)
        write(self.current / "bin" / "norn-api", "api-binary", 0o755)
        write(self.current / "bin" / "norn-host-agent", "host-agent-binary", 0o755)
        self.managed_api = self.root / "go" / "bin" / "norn-api"
        self.managed_agent = self.root / "host" / "bin" / "norn-host-agent"
        self.managed_api.parent.mkdir(parents=True)
        self.managed_agent.parent.mkdir(parents=True)
        shutil.copy2(self.current / "bin" / "norn-api", self.managed_api)
        shutil.copy2(self.current / "bin" / "norn-host-agent", self.managed_agent)
        self.fence = write_json(self.root / "go" / "bin" / ".norn-legacy-baseline-fence.json", {
            "schema": "norn.legacy-baseline-fence/v1", "state": "candidate-postflight-failed", "legacyReleaseSHA": LEGACY,
        })
        self.ledger = write_json(self.home / ".config" / "norn" / "ledger.json", {
            "schema": "norn.m5-protected-transition/v1", "state": "candidate-recovery-required",
            "legacyReleaseSHA": LEGACY, "candidateSHA": CANDIDATE, "upgradeExitCode": 1,
            "beforeDigest": "b" * 64,
        })
        self.reconciliation = write_json(self.home / ".config" / "norn" / "reconciliation.json", {
            "schema": "norn.m5-post-transition-reconciliation/v1",
            "release": {"sha": CANDIDATE, "verified": True},
            "transition": {"beforeDigest": "b" * 64, "ledgerState": "candidate-recovery-required",
                           "matchesBefore": False, "upgradeExitCode": 1},
        })
        self.binding = write_json(self.home / ".config" / "norn" / "binding.json", {
            "schema": "norn.recovered-baseline-incident-binding/v1", "candidateSHA": CANDIDATE,
            "legacyFence": self.item(self.fence), "ledger": self.item(self.ledger),
            "reconciliation": self.item(self.reconciliation),
        })
        self.reason = write_json(self.home / ".config" / "norn" / "reason.json", {
            "reason": "Accept recovered signed runtime as a new baseline; historical preservation is unproven."
        })
        self.receipt = self.home / ".config" / "norn" / ".norn-recovered-baseline-adoption.json"
        shutil.copy2(SCRIPT, self.adopter / "bin" / "platform-upgrade")
        self.shims = self.root / "shims"
        self.shims.mkdir()
        self.repo = self.root / "repo"
        self.repo.mkdir()
        subprocess.run(["git", "init", "-q", str(self.repo)], check=True)
        (self.repo / "v2" / "api").mkdir(parents=True)
        (self.repo / "v2" / "cli").mkdir(parents=True)
        subprocess.run(["git", "-C", str(self.repo), "-c", "user.name=Fixture", "-c",
                        "user.email=fixture@example.invalid", "commit", "-q", "--allow-empty",
                        "-m", "Fixture"], check=True)
        subprocess.run(["git", "-C", str(self.repo), "tag", "v2.20.0-platform"], check=True)
        write(self.adopter / "bin" / "platform-release-manifest", "#!/usr/bin/env python3\nprint('signed')\n", 0o755)
        self.verify = write(self.shims / "verify", "#!/bin/sh\nexit 0\n", 0o755)
        write(self.shims / "launchctl", "#!/bin/sh\nprintf 'service = {\\n  pid = 4242\\n}\\n'\n", 0o755)
        write(self.shims / "lsof", "#!/bin/sh\necho 4242\n", 0o755)
        write(self.shims / "curl", f"#!/bin/sh\necho '{{\"version\":\"{VERSION}\"}}'\n", 0o755)
        self.env = os.environ.copy()
        self.env.update({
            "HOME": str(self.home), "PATH": str(self.shims) + os.pathsep + os.environ["PATH"],
            "NORN_RELEASES_DIR": str(self.releases), "NORN_CURRENT_LINK": str(self.current_link),
            "NORN_LEGACY_API_PATH": str(self.managed_api), "NORN_HOST_AGENT_BIN": str(self.managed_agent),
            "NORN_LEGACY_FENCE_STATE_PATH": str(self.fence), "NORN_RELEASE_VERIFY_HOOK": str(self.verify),
            "NORN_RELEASE_MANIFEST_HELPER": str(self.adopter / "bin" / "platform-release-manifest"),
            "NORN_AUDIT_SIGNING_KEY": "fixture-audit-signing-key-32-characters", "NORN_API_BASE": "http://127.0.0.1:8800",
            "NORN_PLATFORM_REPO": str(self.repo),
        })

    @staticmethod
    def item(path: Path) -> dict:
        return {"path": str(path), "sha256": hashlib.sha256(path.read_bytes()).hexdigest()}

    def run_script(self, *args: str) -> subprocess.CompletedProcess:
        return subprocess.run([str(self.adopter / "bin" / "platform-upgrade"), *args],
                              env=self.env, text=True, capture_output=True, timeout=15)

    def adopt(self) -> subprocess.CompletedProcess:
        return self.run_script("recovered-baseline-adopt", "--incident-binding", str(self.binding),
                               "--receipt", str(self.receipt), "--expected-current-sha", CANDIDATE,
                               "--accept-preservation-unproven", "--reason-file", str(self.reason))

    def test_signed_candidate_adopts_e730_version_without_source_sha(self):
        result = self.adopt()
        self.assertEqual(result.returncode, 0, result.stderr)
        receipt = json.loads(self.receipt.read_text())
        self.assertIs(receipt["preservationProven"], False)
        self.assertEqual(receipt["candidateSHA"], CANDIDATE)
        self.assertEqual(stat.S_IMODE(self.receipt.stat().st_mode), 0o600)
        # An otherwise valid preflight can move past the incident gate. The
        # deliberately absent repo then stops it before release preparation.
        next_step = self.run_script("preflight", "HEAD")
        self.assertNotIn("adoption receipt is required", next_step.stderr)
        self.assertNotEqual(next_step.returncode, 0)
        replay = self.run_script("legacy-baseline-finalize", "--transition-id", "fabricated")
        self.assertNotEqual(replay.returncode, 0)
        self.assertIn("permanently rejects legacy-baseline replay", replay.stderr)

    def test_missing_or_tampered_receipt_fails_before_release_preparation(self):
        missing = self.run_script("preflight", "HEAD")
        self.assertNotEqual(missing.returncode, 0)
        self.assertIn("adoption receipt is required", missing.stderr)
        self.assertEqual(self.adopt().returncode, 0)
        self.fence.write_text(self.fence.read_text() + " ")
        altered = self.run_script("preflight", "HEAD")
        self.assertNotEqual(altered.returncode, 0)
        self.assertIn("adoption receipt is required", altered.stderr)

    def test_failed_fence_blocks_early_dispatch_but_keeps_canonical_agent_startable(self):
        arbitrary = self.run_script("env-exec", "/bin/sh", "-c", "exit 0")
        self.assertNotEqual(arbitrary.returncode, 0)
        self.assertIn("rejects env-exec child mode", arbitrary.stderr)
        proxy = self.run_script("proxy-switch", "19999")
        self.assertNotEqual(proxy.returncode, 0)
        self.assertIn("blocks proxy switching", proxy.stderr)

        agent_body = "#!/bin/sh\nexit 23\n"
        write(self.current / "bin" / "norn-host-agent", agent_body, 0o755)
        write(self.managed_agent, agent_body, 0o755)
        api_env = write(self.home / ".config" / "norn" / "api.env.enc.json", "encrypted")
        sops = write(self.shims / "sops", "#!/bin/sh\nprintf '{}\\n'\n", 0o755)
        self.env.update({"NORN_API_ENV_FILE": str(api_env), "NORN_SOPS_BIN": str(sops),
                         "NORN_PLATFORM_SCRIPT_BIN": str(self.root / "host" / "bin" / "platform-upgrade"),
                         "NORN_HOST_RUNTIME_BIN": str(self.root / "host" / "bin" / "host-runtime")})
        canonical = self.run_script("env-exec", str(self.managed_agent), "--repo", str(self.repo),
                                    "--platform-script", self.env["NORN_PLATFORM_SCRIPT_BIN"],
                                    "--host-script", self.env["NORN_HOST_RUNTIME_BIN"])
        self.assertEqual(canonical.returncode, 23, canonical.stderr)

    def test_adoption_runs_from_signed_release_through_managed_env_wrapper(self):
        managed_script = write(self.root / "host" / "bin" / "platform-upgrade",
                               SCRIPT.read_bytes(), 0o755)
        api_env = write(self.home / ".config" / "norn" / "api.env.enc.json", "encrypted")
        sops = write(self.shims / "sops", "#!/bin/sh\nprintf '{\"NORN_AUDIT_SIGNING_KEY\":\"fixture-audit-signing-key-32-characters\"}\\n'\n", 0o755)
        self.env.update({"NORN_API_ENV_FILE": str(api_env), "NORN_SOPS_BIN": str(sops),
                         "NORN_PLATFORM_SCRIPT_BIN": str(managed_script)})
        self.env.pop("NORN_AUDIT_SIGNING_KEY", None)
        command = [str(managed_script), "env-exec", "--", str(self.adopter / "bin" / "platform-upgrade"),
                   "recovered-baseline-adopt", "--incident-binding", str(self.binding),
                   "--receipt", str(self.receipt), "--expected-current-sha", CANDIDATE,
                   "--accept-preservation-unproven", "--reason-file", str(self.reason)]
        result = subprocess.run(command, env=self.env, text=True, capture_output=True, timeout=15)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(self.receipt.exists())

        escaped = self.releases / ".." / "escape" / "bin" / "platform-upgrade"
        write(escaped, "#!/bin/sh\nexit 0\n", 0o755)
        escaped_command = command.copy()
        escaped_command[3] = str(escaped)
        rejected = subprocess.run(escaped_command, env=self.env, text=True,
                                  capture_output=True, timeout=15)
        self.assertNotEqual(rejected.returncode, 0)
        self.assertIn("not directly inside immutable releases", rejected.stderr)


if __name__ == "__main__":
    unittest.main()
