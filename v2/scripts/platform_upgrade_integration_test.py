#!/usr/bin/env python3

import os
import shutil
import stat
import subprocess
import tempfile
import time
import unittest
from pathlib import Path


SCRIPT_DIR = Path(__file__).resolve().parent


class PlatformUpgradeIntegrationTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.repo = self.root / "repo"
        self.releases = self.root / "releases"
        self.logs = self.root / "logs"
        self.repo.mkdir()
        self.write_fixture_repo()
        self.git("init")
        self.git("add", ".")
        self.git(
            "-c", "user.name=Norn Test", "-c", "user.email=norn@example.invalid",
            "commit", "-m", "fixture",
        )
        self.sha = self.git("rev-parse", "HEAD").stdout.strip()

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def write(self, relative: str, content: str, executable: bool = False) -> None:
        path = self.repo / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8")
        if executable:
            path.chmod(0o755)

    def write_fixture_repo(self) -> None:
        scripts = self.repo / "v2/scripts"
        scripts.mkdir(parents=True)
        for name in (
            "platform-upgrade",
            "platform-release-manifest",
            "platform-release-artifact",
            "platform-release-fetch-github",
            "platform-release-verify-github",
        ):
            shutil.copy2(SCRIPT_DIR / name, scripts / name)
        self.write("v2/scripts/host-runtime", "#!/usr/bin/env bash\nexit 0\n", executable=True)
        self.write(
            "v2/api/go.mod",
            "module norn/v2/api\n\ngo 1.26\n",
        )
        self.write(
            "v2/api/main.go",
            "package main\n\nvar Version = \"dev\"\n\nfunc main() {}\n",
        )
        self.write(
            "v2/api/cmd/norn-host-agent/main.go",
            "package main\n\nfunc main() {}\n",
        )
        self.write(
            "v2/cli/go.mod",
            "module norn/v2/cli\n\ngo 1.26\n",
        )
        self.write(
            "v2/cli/main.go",
            "package main\n\nimport _ \"norn/v2/cli/cmd\"\n\nfunc main() {}\n",
        )
        self.write("v2/cli/cmd/version.go", "package cmd\n\nvar Version = \"dev\"\n")
        self.write(
            "v2/ui/package.json",
            '{"private":true,"packageManager":"pnpm@10.32.1","engines":{"node":"24.19.0"},'
            '"scripts":{"build":"node build.mjs"}}\n',
        )
        self.write(
            "v2/ui/pnpm-lock.yaml",
            "lockfileVersion: '9.0'\n\nsettings:\n  autoInstallPeers: true\n  excludeLinksFromLockfile: false\n\nimporters:\n  .: {}\n",
        )
        self.write(
            "v2/ui/build.mjs",
            'import { mkdirSync, writeFileSync } from "node:fs";\n'
            'mkdirSync("dist", { recursive: true });\n'
            'writeFileSync("dist/index.html", "<main>Norn</main>\\n");\n',
        )

    def git(self, *arguments: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            ["git", "-C", str(self.repo), *arguments],
            check=True,
            capture_output=True,
            text=True,
        )

    def platform(
        self,
        *arguments: str,
        expect_success: bool = True,
        extra_environment: dict[str, str] | None = None,
    ) -> subprocess.CompletedProcess[str]:
        result = subprocess.run(
            [str(self.repo / "v2/scripts/platform-upgrade"), *arguments],
            env=self.platform_environment(extra_environment),
            check=False,
            capture_output=True,
            text=True,
        )
        if expect_success and result.returncode != 0:
            self.fail(f"platform-upgrade failed:\nstdout:\n{result.stdout}\nstderr:\n{result.stderr}")
        return result

    def platform_environment(self, extra_environment: dict[str, str] | None = None) -> dict[str, str]:
        environment = os.environ.copy()
        environment.update(
            {
                "NORN_PLATFORM_REPO": str(self.repo),
                "NORN_RELEASES_DIR": str(self.releases),
                "NORN_RELEASE_LOGS_DIR": str(self.logs),
                "NORN_CURRENT_LINK": str(self.root / "current"),
                "NORN_BIN_DIR": str(self.root / "bin"),
                "NORN_HOST_AGENT_BIN": str(self.root / "host/norn-host-agent"),
                "NORN_HOST_CLI_BIN": str(self.root / "host/norn"),
                "NORN_PLATFORM_SCRIPT_BIN": str(self.root / "host/platform-upgrade"),
                "NORN_HOST_RUNTIME_BIN": str(self.root / "host/host-runtime"),
                "NORN_PROXY_DIR": str(self.root / "proxy"),
                "NORN_SKIP_CANDIDATE_API": "true",
                "NORN_NODE_BIN": shutil.which("node") or "node",
            }
        )
        environment.update(extra_environment or {})
        return environment

    def test_atomic_immutable_reuse_and_verified_rebuild(self) -> None:
        self.platform("preflight", "HEAD")
        release = self.releases / self.sha
        self.assertTrue((release / "release.json").is_file())
        self.assertFalse((release.stat().st_mode & stat.S_IWUSR) != 0)
        self.assertFalse((release / "logs").exists())
        inode = release.stat().st_ino

        reused = self.platform("preflight", "HEAD")
        self.assertIn("reusing immutable release", reused.stdout)
        self.assertEqual(release.stat().st_ino, inode)

        rebuilt = self.platform("rebuild", self.sha, "--verify")
        self.assertIn("rebuild verified manifest-content equivalence", rebuilt.stdout)
        self.assertEqual(release.stat().st_ino, inode)
        self.assertEqual(list(self.releases.glob(".rebuild-*")), [])

    def test_existing_tampered_release_is_never_replaced(self) -> None:
        self.platform("preflight", "HEAD")
        release = self.releases / self.sha
        binary = release / "bin/norn-api"
        binary.chmod(0o755)
        binary.write_bytes(b"tampered")
        inode = release.stat().st_ino

        result = self.platform("preflight", "HEAD", expect_success=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("release manifest verification failed", result.stderr)
        self.assertEqual(release.stat().st_ino, inode)
        self.assertEqual(binary.read_bytes(), b"tampered")

    def test_release_references_are_strict_and_production_cannot_downgrade_policy(self) -> None:
        traversal = self.platform("rollback", "../../escape", expect_success=False)
        self.assertIn("lowercase hexadecimal SHA", traversal.stderr)

        symbolic = self.platform("rebuild", "HEAD", "--verify", expect_success=False)
        self.assertIn("exact 40-character lowercase commit SHA", symbolic.stderr)

        production = self.platform(
            "preflight",
            "HEAD",
            expect_success=False,
            extra_environment={
                "NORN_PROFILE": "production",
                "NORN_RELEASE_SIGNATURE_POLICY": "allow-unsigned",
                "NORN_ALLOW_LEGACY_RELEASES": "true",
            },
        )
        self.assertIn("production requires NORN_RELEASE_VERIFY_HOOK", production.stderr)

    def test_required_signed_release_is_fetched_before_preflight(self) -> None:
        fetch_marker = self.root / "fetch-called"
        fetch_hook = self.root / "fetch-release"
        fetch_hook.write_text(
            "#!/bin/sh\n"
            "set -eu\n"
            'sha="$1"\n'
            'releases="$2"\n'
            'mkdir -p "$releases/$sha"\n'
            'printf \'{}\\n\' > "$releases/$sha/release.json"\n'
            'printf \'{}\\n\' > "$releases/$sha/release.signature.json"\n'
            f': > "{fetch_marker}"\n',
            encoding="utf-8",
        )
        fetch_hook.chmod(0o755)

        manifest_helper = self.root / "verify-manifest"
        manifest_helper.write_text("#!/usr/bin/env python3\nprint('signed')\n", encoding="utf-8")
        manifest_helper.chmod(0o755)
        verify_hook = self.root / "verify-release"
        verify_hook.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
        verify_hook.chmod(0o755)

        result = self.platform(
            "preflight",
            self.sha,
            extra_environment={
                "NORN_RELEASE_SIGNATURE_POLICY": "require-signed",
                "NORN_RELEASE_FETCH_HOOK": str(fetch_hook),
                "NORN_RELEASE_VERIFY_HOOK": str(verify_hook),
                "NORN_RELEASE_MANIFEST_HELPER": str(manifest_helper),
            },
        )

        self.assertTrue(fetch_marker.exists())
        self.assertTrue((self.releases / self.sha).is_dir())
        self.assertIn("invoking configured fetch hook", result.stdout)
        self.assertIn("reusing immutable release", result.stdout)

    def test_concurrent_promotions_fail_closed_and_lock_recovers(self) -> None:
        self.platform("preflight", "HEAD")
        fake_bin = self.root / "fake-bin"
        fake_bin.mkdir()
        marker = self.root / "launchctl-started"
        launchctl = fake_bin / "launchctl"
        launchctl.write_text(
            "#!/bin/sh\n"
            ': > "$NORN_TEST_LAUNCHCTL_MARKER"\n'
            'sleep "${NORN_TEST_LAUNCHCTL_SLEEP:-0}"\n',
            encoding="utf-8",
        )
        launchctl.chmod(0o755)
        curl = fake_bin / "curl"
        curl.write_text('#!/bin/sh\nprintf \'{}\\n\'\n', encoding="utf-8")
        curl.chmod(0o755)
        environment = self.platform_environment(
            {
                "PATH": str(fake_bin) + os.pathsep + os.environ["PATH"],
                "NORN_DRAIN_MODE": "force",
                "NORN_TEST_LAUNCHCTL_MARKER": str(marker),
                "NORN_TEST_LAUNCHCTL_SLEEP": "2",
            }
        )
        command = [str(self.repo / "v2/scripts/platform-upgrade"), "upgrade", self.sha]
        first = subprocess.Popen(command, env=environment, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        deadline = time.monotonic() + 15
        while not marker.exists() and time.monotonic() < deadline:
            time.sleep(0.05)
        self.assertTrue(marker.exists(), "first promotion did not reach the guarded restart")

        second = subprocess.run(command, env=environment, text=True, capture_output=True, check=False)
        self.assertNotEqual(second.returncode, 0)
        self.assertIn("another platform promotion is already active", second.stderr)

        proxy_switch = subprocess.run(
            [str(self.repo / "v2/scripts/platform-upgrade"), "proxy-switch", "18802"],
            env=environment,
            text=True,
            capture_output=True,
            check=False,
        )
        self.assertNotEqual(proxy_switch.returncode, 0)
        self.assertIn("another platform promotion is already active", proxy_switch.stderr)
        self.assertFalse((self.root / "proxy/upstream").exists())

        first_stdout, first_stderr = first.communicate(timeout=15)
        self.assertEqual(first.returncode, 0, first_stdout + first_stderr)

        marker.unlink()
        recovered_environment = environment | {"NORN_TEST_LAUNCHCTL_SLEEP": "0"}
        recovered = subprocess.run(
            command,
            env=recovered_environment,
            text=True,
            capture_output=True,
            check=False,
        )
        self.assertEqual(recovered.returncode, 0, recovered.stdout + recovered.stderr)

        recovered_switch = subprocess.run(
            [str(self.repo / "v2/scripts/platform-upgrade"), "proxy-switch", "18802"],
            env=recovered_environment,
            text=True,
            capture_output=True,
            check=False,
        )
        self.assertEqual(recovered_switch.returncode, 0, recovered_switch.stdout + recovered_switch.stderr)
        self.assertEqual((self.root / "proxy/upstream").read_text(), "127.0.0.1:18802\n")


if __name__ == "__main__":
    unittest.main()
