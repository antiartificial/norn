#!/usr/bin/env python3

import gzip
import hashlib
import json
import os
import shlex
import shutil
import subprocess
import tarfile
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


SCRIPT_DIR = Path(__file__).resolve().parent
ARTIFACT = SCRIPT_DIR / "platform-release-artifact"
MANIFEST = SCRIPT_DIR / "platform-release-manifest"
VERIFY_ADAPTER = SCRIPT_DIR / "platform-release-verify-github"
SHA = "a" * 40
BINARIES = (
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


class PlatformReleaseArtifactTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.openssl = next(
            (
                candidate
                for candidate in (
                    "/opt/homebrew/opt/openssl@3/bin/openssl",
                    "/usr/local/opt/openssl@3/bin/openssl",
                    shutil.which("openssl"),
                )
                if candidate and Path(candidate).is_file()
            ),
            None,
        )
        if not cls.openssl:
            raise unittest.SkipTest("OpenSSL is required")

    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.private_key = self.root / "release-private.pem"
        self.public_key = self.root / "release-public.pem"
        subprocess.run(
            [self.openssl, "genpkey", "-algorithm", "ED25519", "-out", str(self.private_key)],
            check=True,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        self.openssl_guard = self.root / "openssl-no-release-secrets"
        self.openssl_guard.write_text(
            "#!/bin/sh\n"
            "if [ -n \"${NORN_RELEASE_GITHUB_TOKEN:-}\" ] || "
            "[ -n \"${NORN_RELEASE_GITHUB_TOKEN_FILE:-}\" ] || "
            "[ -n \"${GH_TOKEN:-}\" ] || "
            "[ -n \"${NORN_RELEASE_SIGNING_KEY:-}\" ]; then exit 97; fi\n"
            f"exec {shlex.quote(str(self.openssl))} \"$@\"\n"
        )
        self.openssl_guard.chmod(0o755)
        subprocess.run(
            [self.openssl, "pkey", "-in", str(self.private_key), "-pubout", "-out", str(self.public_key)],
            check=True,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        self.source = self.root / "source"
        self.release = self.root / "release"
        for path in (self.source / "v2/api", self.source / "v2/cli", self.source / "v2/ui"):
            path.mkdir(parents=True)
        (self.source / "v2/api/go.sum").write_text("api\n")
        (self.source / "v2/cli/go.sum").write_text("cli\n")
        (self.source / "v2/ui/pnpm-lock.yaml").write_text("lockfileVersion: '9.0'\n")
        (self.release / "bin").mkdir(parents=True)
        (self.release / "ui/assets").mkdir(parents=True)
        for name in BINARIES:
            path = self.release / "bin" / name
            path.write_bytes((name + "\n").encode())
            path.chmod(0o755)
        (self.release / "ui/index.html").write_text("<main>Norn</main>\n")
        (self.release / "ui/assets/app.js").write_text("export default 1\n")
        (self.release / "release.env").write_text(
            f"NORN_RELEASE_SHA={SHA}\nNORN_RELEASE_VERSION=v2-test\nNORN_RELEASE_CREATED_AT=2026-08-26T20:00:00Z\nNORN_UI_DIR=ui\n"
        )
        self._run(
            MANIFEST,
            "create",
            "--release", str(self.release),
            "--source", str(self.source),
            "--sha", SHA,
            "--version", "v2-test",
            "--created-at", "2026-08-26T20:00:00Z",
            "--os", "linux",
            "--arch", "amd64",
            "--go-version", "go1.test",
            "--node-version", "v24.19.0",
            "--node-required", "v24.19.0",
            "--pnpm-version", "10.32.1",
            "--pnpm-required", "10.32.1",
            "--go-flag=-trimpath",
            "--go-ldflag=-buildid=",
            "--cgo-enabled", "0",
            "--source-date-epoch", "1787792400",
        )
        self.bundle = self.root / "bundle"
        self._run(
            ARTIFACT,
            "package",
            "--release-dir", str(self.release),
            "--output-dir", str(self.bundle),
            "--commit", SHA,
            "--os", "linux",
            "--arch", "amd64",
            "--repository", "antiartificial/norn",
            "--source-date-epoch", "1787792400",
        )
        self.signing_env = os.environ | {
            "NORN_RELEASE_SIGNING_KEY": self.private_key.read_text(),
            "NORN_OPENSSL": str(self.openssl_guard),
        }
        self._run(ARTIFACT, "sign", "--bundle-dir", str(self.bundle), env=self.signing_env)

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def _run(self, executable: Path, *args: str, env=None, success=True):
        result = subprocess.run([str(executable), *args], text=True, capture_output=True, env=env)
        if success and result.returncode != 0:
            self.fail(f"{executable.name} failed: {result.stderr}")
        return result

    def _verify_bundle(self, bundle=None, success=True):
        return self._run(
            ARTIFACT,
            "verify",
            "--bundle-dir", str(bundle or self.bundle),
            "--public-key", str(self.public_key),
            success=success,
        )

    def _rewrite_archive_with_special_entry(self, bundle: Path, traversal: bool) -> None:
        manifest_path = next(bundle.glob("*.manifest.json"))
        manifest = json.loads(manifest_path.read_text())
        archive_path = bundle / manifest["archive"]["name"]
        unpacked = self.root / ("unpacked-traversal" if traversal else "unpacked-link")
        unpacked.mkdir()
        with tarfile.open(archive_path, "r:gz") as archive:
            archive.extractall(unpacked, filter="data")
        with tarfile.open(archive_path, "w:gz") as archive:
            for path in sorted(unpacked.rglob("*")):
                archive.add(path, arcname=path.relative_to(unpacked).as_posix(), recursive=False)
            if traversal:
                entry = tarfile.TarInfo("../escape")
                entry.size = 0
            else:
                entry = tarfile.TarInfo("ui/link")
                entry.type = tarfile.SYMTYPE
                entry.linkname = "index.html"
            archive.addfile(entry)
        manifest["archive"]["size"] = archive_path.stat().st_size
        manifest["archive"]["sha256"] = hashlib.sha256(archive_path.read_bytes()).hexdigest()
        manifest_path.write_text(json.dumps(manifest, sort_keys=True, separators=(",", ":")) + "\n")
        next(bundle.glob("*.manifest.sig")).unlink()
        self._run(ARTIFACT, "sign", "--bundle-dir", str(bundle), env=self.signing_env)

    def test_round_trip_import_passes_local_and_external_verifiers(self) -> None:
        self._verify_bundle()
        releases = self.root / "releases"
        self._run(
            ARTIFACT,
            "import",
            "--bundle-dir", str(self.bundle),
            "--releases-dir", str(releases),
            "--public-key", str(self.public_key),
        )
        installed = releases / SHA
        local = self._run(MANIFEST, "verify", "--release", str(installed), "--expected-sha", SHA)
        self.assertEqual(local.stdout.strip(), "signed")
        external = self._run(
            VERIFY_ADAPTER,
            str(installed),
            str(installed / "release.json"),
            env=os.environ | {"NORN_RELEASE_PUBLIC_KEY": str(self.public_key)},
        )
        self.assertEqual(external.stdout, "")
        duplicate = self._run(
            ARTIFACT,
            "import",
            "--bundle-dir", str(self.bundle),
            "--releases-dir", str(releases),
            "--public-key", str(self.public_key),
            success=False,
        )
        self.assertIn("already exists", duplicate.stderr)

    def test_import_seals_release_root_after_atomic_publication(self) -> None:
        releases = self.root / "sealed-releases"
        self._run(
            ARTIFACT,
            "import",
            "--bundle-dir", str(self.bundle),
            "--releases-dir", str(releases),
            "--public-key", str(self.public_key),
        )
        installed = releases / SHA
        self.assertEqual(installed.stat().st_mode & 0o222, 0)
        self.assertEqual((installed / "bin" / "norn").stat().st_mode & 0o222, 0)

    def test_manifest_signature_and_archive_tampering_are_rejected(self) -> None:
        tampered = self.root / "tampered"
        shutil.copytree(self.bundle, tampered)
        manifest_path = next(tampered.glob("*.manifest.json"))
        manifest_path.write_bytes(manifest_path.read_bytes() + b" ")
        result = self._verify_bundle(tampered, success=False)
        self.assertIn("OpenSSL operation failed", result.stderr)

        tampered = self.root / "tampered-archive"
        shutil.copytree(self.bundle, tampered)
        archive_path = next(tampered.glob("*.tar.gz"))
        with archive_path.open("ab") as stream:
            stream.write(b"tampered")
        result = self._verify_bundle(tampered, success=False)
        self.assertIn("checksum or size mismatch", result.stderr)

    def test_signed_traversal_and_symlink_archives_are_rejected(self) -> None:
        traversal = self.root / "traversal"
        shutil.copytree(self.bundle, traversal)
        self._rewrite_archive_with_special_entry(traversal, traversal=True)
        result = self._verify_bundle(traversal, success=False)
        self.assertIn("unsafe archive path", result.stderr)

        symlink = self.root / "symlink"
        shutil.copytree(self.bundle, symlink)
        self._rewrite_archive_with_special_entry(symlink, traversal=False)
        result = self._verify_bundle(symlink, success=False)
        self.assertIn("links and special files are forbidden", result.stderr)

    def test_package_rejects_source_symlinks_and_never_accepts_token_argv(self) -> None:
        linked_release = self.root / "linked-release"
        shutil.copytree(self.release, linked_release)
        os.symlink("index.html", linked_release / "ui/link.html")
        result = self._run(
            ARTIFACT,
            "package",
            "--release-dir", str(linked_release),
            "--output-dir", str(self.root / "linked-bundle"),
            "--commit", SHA,
            "--os", "linux",
            "--arch", "amd64",
            success=False,
        )
        self.assertIn("symlink", result.stderr)
        help_result = self._run(ARTIFACT, "fetch", "--help")
        self.assertNotIn("token", help_result.stdout.lower())

    def test_private_github_fetch_uses_header_auth_without_disclosure(self) -> None:
        assets = {path.name: path.read_bytes() for path in self.bundle.iterdir() if path.is_file()}
        observed_authorization = []
        observed_redirected_authorization = []

        class AssetHandler(BaseHTTPRequestHandler):
            def do_GET(handler):
                observed_redirected_authorization.append(handler.headers.get("Authorization"))
                if not handler.path.startswith("/assets/"):
                    handler.send_error(404)
                    return
                index = int(handler.path.rsplit("/", 1)[1])
                body = sorted(assets.items())[index][1]
                handler.send_response(200)
                handler.send_header("Content-Type", "application/octet-stream")
                handler.send_header("Content-Length", str(len(body)))
                handler.end_headers()
                handler.wfile.write(body)

            def log_message(self, *_args):
                pass

        class Handler(BaseHTTPRequestHandler):
            def do_GET(handler):
                observed_authorization.append(handler.headers.get("Authorization"))
                if handler.path == "/repos/antiartificial/norn":
                    if handler.headers.get("Authorization") != f"Bearer {token}":
                        handler.send_error(404)
                        return
                    body = json.dumps({"private": True}).encode()
                    content_type = "application/json"
                elif handler.path == f"/repos/antiartificial/norn/releases/tags/platform-{SHA}":
                    body = json.dumps({
                        "tag_name": f"platform-{SHA}",
                        "draft": False,
                        "assets": [
                            {"name": name, "size": len(data), "url": f"{server_url}/assets/{index}"}
                            for index, (name, data) in enumerate(sorted(assets.items()))
                        ],
                    }).encode()
                    content_type = "application/json"
                elif handler.path.startswith("/assets/"):
                    index = int(handler.path.rsplit("/", 1)[1])
                    handler.send_response(302)
                    handler.send_header("Location", f"{asset_server_url}/assets/{index}")
                    handler.end_headers()
                    return
                else:
                    handler.send_error(404)
                    return
                handler.send_response(200)
                handler.send_header("Content-Type", content_type)
                handler.send_header("Content-Length", str(len(body)))
                handler.end_headers()
                handler.wfile.write(body)

            def log_message(self, *_args):
                pass

        asset_server = ThreadingHTTPServer(("127.0.0.1", 0), AssetHandler)
        asset_server_url = f"http://127.0.0.1:{asset_server.server_port}"
        asset_thread = threading.Thread(target=asset_server.serve_forever, daemon=True)
        asset_thread.start()
        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        server_url = f"http://127.0.0.1:{server.server_port}"
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        token = "test-private-token-never-log"
        token_file = self.root / "github-release-token"
        token_file.write_text(token + "\n")
        token_file.chmod(0o600)
        clean_env = {
            key: value for key, value in os.environ.items()
            if key not in {"NORN_RELEASE_GITHUB_TOKEN", "NORN_RELEASE_GITHUB_TOKEN_FILE", "GH_TOKEN"}
        }
        try:
            result = self._run(
                ARTIFACT,
                "fetch",
                "--repository", "antiartificial/norn",
                "--commit", SHA,
                "--cache-dir", str(self.root / "cache"),
                "--public-key", str(self.public_key),
                "--os", "linux",
                "--arch", "amd64",
                env=clean_env | {
                    "NORN_RELEASE_GITHUB_TOKEN_FILE": str(token_file),
                    "GH_TOKEN": "ambient-token-must-not-override-the-explicit-file",
                    "NORN_OPENSSL": str(self.openssl_guard),
                    "NORN_RELEASE_GITHUB_API_URL": server_url,
                    "NORN_RELEASE_ALLOW_INSECURE_LOCALHOST": "1",
                },
            )
            env_result = self._run(
                ARTIFACT,
                "fetch",
                "--repository", "antiartificial/norn",
                "--commit", SHA,
                "--cache-dir", str(self.root / "env-cache"),
                "--public-key", str(self.public_key),
                "--os", "linux",
                "--arch", "amd64",
                env=clean_env | {
                    "NORN_RELEASE_GITHUB_TOKEN": token,
                    "NORN_OPENSSL": str(self.openssl_guard),
                    "NORN_RELEASE_GITHUB_API_URL": server_url,
                    "NORN_RELEASE_ALLOW_INSECURE_LOCALHOST": "1",
                },
            )
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=2)
            asset_server.shutdown()
            asset_server.server_close()
            asset_thread.join(timeout=2)
        self.assertNotIn(token, result.stdout)
        self.assertNotIn(token, result.stderr)
        self.assertNotIn(token, env_result.stdout)
        self.assertNotIn(token, env_result.stderr)
        self.assertTrue(observed_authorization)
        self.assertEqual(set(observed_authorization), {None, f"Bearer {token}"})
        self.assertTrue(observed_redirected_authorization)
        self.assertEqual(set(observed_redirected_authorization), {None})
        fetched = self.root / "cache" / SHA / "linux-amd64"
        self._verify_bundle(fetched)
        self._verify_bundle(self.root / "env-cache" / SHA / "linux-amd64")

    def test_private_token_file_requires_owned_non_symlink_exact_0600_file(self) -> None:
        token_file = self.root / "bad-token"
        token_file.write_text("private-token\n")
        clean_env = {
            key: value for key, value in os.environ.items()
            if key not in {"NORN_RELEASE_GITHUB_TOKEN", "NORN_RELEASE_GITHUB_TOKEN_FILE", "GH_TOKEN"}
        }

        token_file.chmod(0o640)
        result = self._run(
            ARTIFACT,
            "fetch",
            "--repository", "antiartificial/norn",
            "--commit", SHA,
            "--cache-dir", str(self.root / "bad-cache"),
            "--public-key", str(self.public_key),
            env=clean_env | {"NORN_RELEASE_GITHUB_TOKEN_FILE": str(token_file)},
            success=False,
        )
        self.assertIn("exact mode 0600", result.stderr)

        token_file.chmod(0o600)
        token_link = self.root / "token-link"
        token_link.symlink_to(token_file)
        result = self._run(
            ARTIFACT,
            "fetch",
            "--repository", "antiartificial/norn",
            "--commit", SHA,
            "--cache-dir", str(self.root / "link-cache"),
            "--public-key", str(self.public_key),
            env=clean_env | {"NORN_RELEASE_GITHUB_TOKEN_FILE": str(token_link)},
            success=False,
        )
        self.assertIn("non-symlink", result.stderr)

    def test_public_github_fetch_does_not_require_or_send_a_token(self) -> None:
        assets = {path.name: path.read_bytes() for path in self.bundle.iterdir() if path.is_file()}
        observed_authorization = []

        class Handler(BaseHTTPRequestHandler):
            def do_GET(handler):
                observed_authorization.append(handler.headers.get("Authorization"))
                if handler.path == "/repos/antiartificial/norn":
                    body = json.dumps({"private": False}).encode()
                    content_type = "application/json"
                elif handler.path == f"/repos/antiartificial/norn/releases/tags/platform-{SHA}":
                    body = json.dumps({
                        "tag_name": f"platform-{SHA}",
                        "draft": False,
                        "assets": [
                            {"name": name, "size": len(data), "url": f"{server_url}/assets/{index}"}
                            for index, (name, data) in enumerate(sorted(assets.items()))
                        ],
                    }).encode()
                    content_type = "application/json"
                elif handler.path.startswith("/assets/"):
                    index = int(handler.path.rsplit("/", 1)[1])
                    body = sorted(assets.items())[index][1]
                    content_type = "application/octet-stream"
                else:
                    handler.send_error(404)
                    return
                handler.send_response(200)
                handler.send_header("Content-Type", content_type)
                handler.send_header("Content-Length", str(len(body)))
                handler.end_headers()
                handler.wfile.write(body)

            def log_message(self, *_args):
                pass

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        server_url = f"http://127.0.0.1:{server.server_port}"
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        clean_env = {
            key: value for key, value in os.environ.items()
            if key not in {"NORN_RELEASE_GITHUB_TOKEN", "NORN_RELEASE_GITHUB_TOKEN_FILE", "GH_TOKEN"}
        }
        clean_env.update({
            "NORN_RELEASE_GITHUB_API_URL": server_url,
            "NORN_RELEASE_ALLOW_INSECURE_LOCALHOST": "1",
            "NORN_OPENSSL": str(self.openssl_guard),
        })
        try:
            self._run(
                ARTIFACT,
                "fetch",
                "--repository", "antiartificial/norn",
                "--commit", SHA,
                "--cache-dir", str(self.root / "public-cache"),
                "--public-key", str(self.public_key),
                "--os", "linux",
                "--arch", "amd64",
                env=clean_env,
            )
            self._run(
                ARTIFACT,
                "fetch",
                "--repository", "antiartificial/norn",
                "--commit", SHA,
                "--cache-dir", str(self.root / "public-ambient-cache"),
                "--public-key", str(self.public_key),
                "--os", "linux",
                "--arch", "amd64",
                env=clean_env | {"GH_TOKEN": "ambient-public-token-must-not-be-sent"},
            )
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=2)
        self.assertTrue(observed_authorization)
        self.assertEqual(set(observed_authorization), {None})
        self._verify_bundle(self.root / "public-cache" / SHA / "linux-amd64")
        self._verify_bundle(self.root / "public-ambient-cache" / SHA / "linux-amd64")


if __name__ == "__main__":
    unittest.main()
