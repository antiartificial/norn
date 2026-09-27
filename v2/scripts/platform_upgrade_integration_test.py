#!/usr/bin/env python3

import json
import os
import shlex
import shutil
import socket
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
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            self.candidate_port = listener.getsockname()[1]
        self.write_fixture_repo()
        self.git("init")
        self.git("add", ".")
        author_name = subprocess.check_output(["git", "-C", str(SCRIPT_DIR), "log", "-1", "--format=%an"], text=True).strip()
        author_email = subprocess.check_output(["git", "-C", str(SCRIPT_DIR), "log", "-1", "--format=%ae"], text=True).strip()
        self.fixture_author = (f"user.name={author_name}", f"user.email={author_email}")
        self.git(
            "-c", self.fixture_author[0], "-c", self.fixture_author[1],
            "commit", "-m", "fixture",
        )
        self.sha = self.git("rev-parse", "HEAD").stdout.strip()
        self.git("tag", "v2.21.0-control")

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
            '''package main

import (
    "encoding/json"
    "fmt"
    "net/http"
    "os"
)

var Version = "dev"

func main() {
    if len(os.Args) == 2 && os.Args[1] == "--norn-startup-contract" {
        fmt.Println(`{"name":"norn.startup/v2","schemaModes":["auto","check","migrate-only"],"startupModes":["active","passive"],"passiveRoutes":["/api/health","/api/version","/api/schema"],"schemaContract":{"readerVersion":1,"writerVersion":1,"catalogMigrationVersion":1,"catalogMinimumReaderVersion":1,"catalogMinimumWriterVersion":1}}`)
        return
    }
    mode := os.Getenv("NORN_STARTUP_MODE")
    if mode == "" { mode = "active" }
    schemaMode := os.Getenv("NORN_SCHEMA_MODE")
    if schemaMode == "" { schemaMode = "check" }
    active := mode == "active"
    http.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(map[string]any{"status":mode}) })
    http.HandleFunc("/api/version", func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(map[string]any{"version":Version}) })
    http.HandleFunc("/api/schema", func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(map[string]any{
        "startupMode":mode,"schemaMode":schemaMode,"currentMigrationVersion":1,
        "operationRecoveryEnabled":active,"operationWorkerEnabled":active,"nomadWatcherEnabled":active,
    }) })
    address := os.Getenv("NORN_BIND_ADDR")
    if address == "" { address = "127.0.0.1" }
    if err := http.ListenAndServe(address+":"+os.Getenv("NORN_PORT"), nil); err != nil { panic(err) }
}
''',
        )
        self.write(
            "v2/api/cmd/norn-host-agent/main.go",
            "package main\n\nfunc main() {}\n",
        )
        self.write(
            "v2/api/cmd/norn-ingress-observer/main.go",
            "package main\n\nfunc main() {}\n",
        )
        self.write(
            "v2/api/cmd/norn-effect-runner/main.go",
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
                "NORN_CANDIDATE_PORT": str(self.candidate_port),
                "NORN_NODE_BIN": shutil.which("node") or "node",
            }
        )
        environment.update(extra_environment or {})
        return environment

    def test_shell_pnpm_launcher_uses_pinned_node(self) -> None:
        actual_pnpm = shutil.which("pnpm")
        self.assertIsNotNone(actual_pnpm)
        fake_bin = self.root / "shell-pnpm"
        fake_bin.mkdir()
        shim = fake_bin / "pnpm"
        shim.write_text(
            "#!/bin/sh\n"
            f"exec {shlex.quote(actual_pnpm)} \"$@\"\n",
            encoding="utf-8",
        )
        shim.chmod(0o755)
        result = self.platform(
            "preflight", "HEAD",
            extra_environment={"PATH": str(fake_bin) + os.pathsep + os.environ["PATH"]},
        )
        self.assertIn("preflight complete", result.stdout)

    def test_atomic_immutable_reuse_and_verified_rebuild(self) -> None:
        self.platform("preflight", "HEAD")
        release = self.releases / self.sha
        self.assertTrue((release / "release.json").is_file())
        manifest = json.loads((release / "release.json").read_text(encoding="utf-8"))
        self.assertEqual(manifest["version"], "v2.21.0-platform")
        self.assertFalse((release.stat().st_mode & stat.S_IWUSR) != 0)
        self.assertFalse((release / "logs").exists())
        inode = release.stat().st_ino

        reused = self.platform("preflight", "HEAD")
        self.assertIn("reusing immutable release", reused.stdout)
        self.assertEqual(release.stat().st_ino, inode)

        self.git("tag", "platform-published-after-build")
        listed = self.platform("releases")
        self.assertIn("VERSION\tSHA\tCREATED\tCURRENT\tPATH", listed.stdout)
        self.assertIn(f"v2.21.0-platform\t{self.sha[:12]}", listed.stdout)
        rebuilt = self.platform("rebuild", self.sha, "--verify")
        self.assertIn("rebuild verified manifest-content equivalence", rebuilt.stdout)
        self.assertEqual(release.stat().st_ino, inode)
        self.assertEqual(list(self.releases.glob(".rebuild-*")), [])

        manifest_wrapper = self.root / "signed-retained-manifest"
        manifest_wrapper.write_text(
            "#!/usr/bin/env python3\n"
            "import subprocess, sys\n"
            f"real = {str(self.repo / 'v2/scripts/platform-release-manifest')!r}\n"
            "result = subprocess.run([sys.executable, real, *sys.argv[1:]], capture_output=True, text=True)\n"
            "if result.returncode != 0:\n"
            "    sys.stderr.write(result.stderr)\n"
            "    raise SystemExit(result.returncode)\n"
            "if sys.argv[1] == 'verify' and not any('.rebuild-' in arg for arg in sys.argv):\n"
            "    print('signed')\n"
            "else:\n"
            "    sys.stdout.write(result.stdout)\n",
            encoding="utf-8",
        )
        manifest_wrapper.chmod(0o755)
        verify_hook = self.root / "verify-signed-release"
        verify_hook.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
        verify_hook.chmod(0o755)

        signed_policy_rebuild = self.platform(
            "rebuild",
            self.sha,
            "--verify",
            extra_environment={
                "NORN_RELEASE_SIGNATURE_POLICY": "require-signed",
                "NORN_RELEASE_VERIFY_HOOK": str(verify_hook),
                "NORN_RELEASE_MANIFEST_HELPER": str(manifest_wrapper),
            },
        )
        self.assertIn("verified rebuilt release", signed_policy_rebuild.stdout)
        self.assertIn("rebuild verified manifest-content equivalence", signed_policy_rebuild.stdout)

    def test_transport_tag_never_replaces_version_first_base(self) -> None:
        self.git("tag", f"platform-{self.sha}")
        self.write("release-marker.txt", "next platform build\n")
        self.git("add", "release-marker.txt")
        self.git(
            "-c", self.fixture_author[0], "-c", self.fixture_author[1],
            "commit", "-m", "next platform build",
        )
        next_sha = self.git("rev-parse", "HEAD").stdout.strip()

        self.platform("preflight", next_sha)

        manifest = json.loads(
            (self.releases / next_sha / "release.json").read_text(encoding="utf-8")
        )
        self.assertEqual(
            manifest["version"],
            f"v2.21.0-platform-1-g{next_sha[:7]}",
        )

    def test_release_build_requires_a_semantic_version_base(self) -> None:
        self.git("tag", "-d", "v2.21.0-control")

        result = self.platform("preflight", "HEAD", expect_success=False)

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("release commit has no reachable version tag", result.stderr)

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
            expect_success=False,
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
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("startup contract probe target is not executable", result.stderr)

    def test_signed_release_fetch_reaches_passive_preflight(self) -> None:
        self.platform("preflight", "HEAD")
        artifact = self.repo / "v2/scripts/platform-release-artifact"
        bundle = self.root / "signed-bundle"
        source_epoch = self.git("show", "-s", "--format=%ct", self.sha).stdout.strip()
        package = subprocess.run(
            [str(artifact), "package", "--release-dir", str(self.releases / self.sha),
             "--output-dir", str(bundle), "--commit", self.sha,
             "--os", subprocess.check_output(["go", "env", "GOOS"], text=True).strip(),
             "--arch", subprocess.check_output(["go", "env", "GOARCH"], text=True).strip(),
             "--repository", "antiartificial/norn", "--source-date-epoch", source_epoch],
            text=True, capture_output=True, check=False,
        )
        self.assertEqual(package.returncode, 0, package.stdout + package.stderr)
        private_key = self.root / "signing-private.pem"
        public_key = self.root / "signing-public.pem"
        openssl = next(
            path for path in (
                "/opt/homebrew/opt/openssl@3/bin/openssl",
                "/usr/local/opt/openssl@3/bin/openssl",
                shutil.which("openssl"),
            ) if path and Path(path).is_file()
        )
        subprocess.run([openssl, "genpkey", "-algorithm", "ED25519", "-out", str(private_key)],
                       check=True, capture_output=True)
        subprocess.run([openssl, "pkey", "-in", str(private_key), "-pubout", "-out", str(public_key)],
                       check=True, capture_output=True)
        signed = subprocess.run(
            [str(artifact), "sign", "--bundle-dir", str(bundle)],
            env=os.environ | {"NORN_RELEASE_SIGNING_KEY_FILE": str(private_key), "NORN_OPENSSL": openssl},
            text=True, capture_output=True, check=False,
        )
        self.assertEqual(signed.returncode, 0, signed.stdout + signed.stderr)

        fetched_releases = self.root / "fetched-releases"
        fetch_hook = self.root / "import-signed-release"
        fetch_hook.write_text(
            "#!/bin/sh\nset -eu\n"
            f"test \"$1\" = {shlex.quote(self.sha)}\n"
            f"exec {shlex.quote(str(artifact))} import --bundle-dir {shlex.quote(str(bundle))} "
            f"--releases-dir \"$2\" --public-key {shlex.quote(str(public_key))}\n",
            encoding="utf-8",
        )
        fetch_hook.chmod(0o755)
        result = self.platform(
            "preflight", self.sha,
            extra_environment={
                "NORN_RELEASES_DIR": str(fetched_releases),
                "NORN_RELEASE_SIGNATURE_POLICY": "require-signed",
                "NORN_RELEASE_FETCH_HOOK": str(fetch_hook),
                "NORN_RELEASE_VERIFY_HOOK": str(self.repo / "v2/scripts/platform-release-verify-github"),
                "NORN_RELEASE_PUBLIC_KEY": str(public_key),
                "NORN_OPENSSL": openssl,
            },
        )
        self.assertIn("preflight complete", result.stdout)
        self.assertIn("verified release", result.stdout)
        self.assertTrue((fetched_releases / self.sha / "signatures").is_dir())

    def test_concurrent_promotions_fail_closed_and_lock_recovers(self) -> None:
        fake_bin = self.root / "fake-bin"
        fake_bin.mkdir()
        marker = self.root / "caddy-reload-started"
        caddy = fake_bin / "caddy"
        caddy.write_text(
            "#!/bin/sh\n"
            ': > "$NORN_TEST_CADDY_MARKER"\n'
            'sleep "${NORN_TEST_CADDY_SLEEP:-0}"\n',
            encoding="utf-8",
        )
        caddy.chmod(0o755)
        environment = self.platform_environment(
            {
                "PATH": str(fake_bin) + os.pathsep + os.environ["PATH"],
                "NORN_PROXY_RELOAD": "true",
                "NORN_TEST_CADDY_MARKER": str(marker),
                "NORN_TEST_CADDY_SLEEP": "2",
            }
        )
        command = [str(self.repo / "v2/scripts/platform-upgrade"), "proxy-switch", "18801"]
        first = subprocess.Popen(command, env=environment, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        deadline = time.monotonic() + 15
        while not marker.exists() and time.monotonic() < deadline:
            time.sleep(0.05)
        self.assertTrue(marker.exists(), "first promotion did not reach the guarded proxy reload")

        second = subprocess.run(
            [str(self.repo / "v2/scripts/platform-upgrade"), "proxy-switch", "18802"],
            env=environment, text=True, capture_output=True, check=False,
        )
        self.assertNotEqual(second.returncode, 0)
        self.assertIn("another platform promotion is already active", second.stderr)

        self.assertEqual((self.root / "proxy/upstream").read_text(), "127.0.0.1:18801\n")

        first_stdout, first_stderr = first.communicate(timeout=15)
        self.assertEqual(first.returncode, 0, first_stdout + first_stderr)

        marker.unlink()
        recovered_environment = environment | {"NORN_TEST_CADDY_SLEEP": "0"}
        recovered = subprocess.run(
            [str(self.repo / "v2/scripts/platform-upgrade"), "proxy-switch", "18802"],
            env=recovered_environment,
            text=True,
            capture_output=True,
            check=False,
        )
        self.assertEqual(recovered.returncode, 0, recovered.stdout + recovered.stderr)

        self.assertEqual((self.root / "proxy/upstream").read_text(), "127.0.0.1:18802\n")


if __name__ == "__main__":
    unittest.main()
