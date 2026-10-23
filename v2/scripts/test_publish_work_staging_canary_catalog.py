import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
PUBLISHER = ROOT / "v2/scripts/publish-work-staging-canary-catalog"
SOURCE = ROOT / "v2/infra/fleet-staging/canary/infraspec.yaml"


class CanaryCatalogPublisherTest(unittest.TestCase):
    def run_publisher(self, source: str) -> tuple[subprocess.CompletedProcess[str], Path, str]:
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        spec = root / "infraspec.yaml"
        spec.write_text(source)
        output = root / "output"
        env = {**os.environ, "GITHUB_SHA": "a" * 40, "GITHUB_RUN_ID": "17",
               "GITHUB_RUN_ATTEMPT": "1", "GITHUB_OUTPUT": str(output),
               "NORN_CATALOG_VALIDATOR": "/usr/bin/true"}
        result = subprocess.run([str(PUBLISHER), str(spec), str(root)],
                                env=env, text=True, capture_output=True, timeout=15)
        return result, root, output.read_text() if output.exists() else ""

    def enabled_source(self) -> str:
        return SOURCE.read_text().replace("deploy: false", "deploy: true").replace(
            "work-staging-canary.invalid", "canary.staging.slopistry.com")

    def test_disabled_source_publishes_nothing(self) -> None:
        result, root, output = self.run_publisher(SOURCE.read_text())
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(output, "publish=false\n")
        self.assertFalse((root / "catalog.tar.gz").exists())

    def test_enabled_source_binds_exact_canary_members(self) -> None:
        result, root, output = self.run_publisher(self.enabled_source())
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("publish=true\n", output)
        with tarfile.open(root / "catalog.tar.gz", "r:gz") as archive:
            self.assertEqual(archive.getnames(), ["canary", "canary/infraspec.yaml",
                                                   "norn-catalog-provenance.json"])
            self.assertIn(b'"appID":"norn-fleet-canary"',
                          archive.extractfile("norn-catalog-provenance.json").read())

    def test_rejects_cross_app_and_unreviewed_route(self) -> None:
        source = self.enabled_source()
        for changed in (source.replace("name: norn-fleet-canary", "name: other-app"),
                        source.replace("nodePool: app", "nodePool: ingress"),
                        source.replace("canary.staging.slopistry.com", "canary.invalid"),
                        source.replace("  dockerfile:", "  image: ghcr.io/other/app:latest\n  dockerfile:")):
            with self.subTest(changed=changed[:80]):
                result, root, output = self.run_publisher(changed)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse((root / "catalog.tar.gz").exists())
                self.assertNotIn("publish=true", output)


if __name__ == "__main__":
    unittest.main()
