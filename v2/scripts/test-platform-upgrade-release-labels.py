#!/usr/bin/env python3
"""Contract tests for signed release labels recognized by platform-upgrade."""

import os
from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("platform-upgrade")


class PlatformUpgradeReleaseLabelTests(unittest.TestCase):
    def repository(self, root, tag, distance=False):
        repo = root / "repo"
        repo.mkdir()
        (repo / "v2/api").mkdir(parents=True)
        (repo / "v2/cli").mkdir(parents=True)
        (repo / "source.txt").write_text("tag target\n")
        subprocess.run(["git", "init", "-q", str(repo)], check=True)
        subprocess.run(["git", "-C", str(repo), "-c", "user.name=Test", "-c",
                        "user.email=test@example.invalid", "add", "."], check=True)
        subprocess.run(["git", "-C", str(repo), "-c", "user.name=Test", "-c",
                        "user.email=test@example.invalid", "commit", "-qm", "fixture"], check=True)
        subprocess.run(["git", "-C", str(repo), "tag", tag], check=True)
        if distance:
            (repo / "distance.txt").write_text("one commit past the tag\n")
            subprocess.run(["git", "-C", str(repo), "add", "distance.txt"], check=True)
            subprocess.run(["git", "-C", str(repo), "-c", "user.name=Test", "-c",
                            "user.email=test@example.invalid", "commit", "-qm", "distance"], check=True)
        return repo

    def invoke(self, root, repo, mode="preflight"):
        releases = root / "releases"
        env = {
            **os.environ,
            "HOME": str(root),
            "NORN_PLATFORM_REPO": str(repo),
            "NORN_RELEASES_DIR": str(releases),
            "NORN_RELEASE_SIGNATURE_POLICY": "require-signed",
            "NORN_RELEASE_CONFIG_FILE": str(root / "missing-release-config"),
        }
        # An explicitly configured absent file is rejected before Git label
        # validation. Use a private HOME with no config file instead.
        env.pop("NORN_RELEASE_CONFIG_FILE")
        return subprocess.run([str(SCRIPT), mode, "HEAD"], env=env, text=True,
                              capture_output=True, timeout=15)

    def test_accepts_canonical_v3_rc_and_existing_stable_labels(self):
        for tag, distance, expected in (
            ("v3.0.0-rc.1-platform", False, "v3.0.0-rc.1-platform"),
            ("v3.4.12-rc.20-platform", True, None),
            ("v2.20.0-platform", False, "v2.20.0-platform"),
        ):
            with self.subTest(tag=tag, distance=distance), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                repo = self.repository(root, tag, distance)
                result = self.invoke(root, repo)
                # Passing label validation proceeds to the deliberately absent
                # signed-release fetch hook; malformed labels fail earlier.
                self.assertNotIn("release tag does not produce", result.stderr)
                self.assertIn("NORN_RELEASE_FETCH_HOOK is not configured", result.stderr)
                self.assertNotEqual(result.returncode, 0)
                if expected:
                    sha = subprocess.check_output(["git", "-C", str(repo), "rev-parse", "HEAD"], text=True).strip()
                    (root / "releases" / sha).mkdir(parents=True)
                    (root / "releases" / sha / "release.env").write_text(
                        f"NORN_RELEASE_VERSION={tag}\n", encoding="utf-8")
                    listed = subprocess.run([str(SCRIPT), "releases"], env={
                        **os.environ, "HOME": str(root), "NORN_PLATFORM_REPO": str(repo),
                        "NORN_RELEASES_DIR": str(root / "releases")}, text=True,
                        capture_output=True, timeout=15)
                    self.assertEqual(listed.returncode, 0, listed.stderr)
                    self.assertIn(expected, listed.stdout)

    def test_git_distance_rc_suffix_is_canonicalized(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            repo = self.repository(root, "v3.4.12-rc.20-platform", distance=True)
            result = self.invoke(root, repo)
            self.assertIn("NORN_RELEASE_FETCH_HOOK is not configured", result.stderr)
            sha = subprocess.check_output(["git", "-C", str(repo), "rev-parse", "HEAD"], text=True).strip()
            (root / "releases" / sha).mkdir(parents=True)
            (root / "releases" / sha / "release.env").write_text(
                "NORN_RELEASE_VERSION=v0.0.1-platform\n", encoding="utf-8")
            listed = subprocess.run([str(SCRIPT), "releases"], env={
                **os.environ, "HOME": str(root), "NORN_PLATFORM_REPO": str(repo),
                "NORN_RELEASES_DIR": str(root / "releases")}, text=True,
                capture_output=True, timeout=15)
            described = subprocess.check_output(
                ["git", "-C", str(repo), "describe", "--tags", "--exclude=platform-*", "HEAD"],
                text=True).strip()
            self.assertEqual(listed.returncode, 0, listed.stderr)
            self.assertIn(described, listed.stdout)
            self.assertRegex(described, r"^v3\.4\.12-rc\.20-platform-1-g[0-9a-f]{7,40}$")

    def test_rejects_malformed_rc_labels(self):
        for tag in ("v3.0.0-rc.0-platform", "v3.0.0-rc.x-platform",
                    "v3.0.0-rc.1.2-platform", "v3.0.0-rc.1-platform-extra"):
            with self.subTest(tag=tag), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                repo = self.repository(root, tag)
                result = self.invoke(root, repo)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("release tag does not produce a supported version-first platform label", result.stderr)


if __name__ == "__main__":
    unittest.main()
