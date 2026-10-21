import unittest
from pathlib import Path


class StagingContractTests(unittest.TestCase):
    def test_disabled_infraspec_and_direct_jobs_pin_app_pool(self):
        root = Path(__file__).parent / "hello-norn-mysql"
        spec = (root / "infraspec.yaml").read_text()
        service = (root / "nomad/hello-norn-mysql.nomad.hcl").read_text()
        migration = (root / "nomad/hello-norn-mysql-migrate.nomad.hcl").read_text()
        self.assertIn("deploy: false", spec)
        self.assertIn("nodePool: app", spec)
        self.assertIn("      max: 4", spec)
        for job in (service, migration):
            self.assertIn('node_pool   = "app"', job)
            self.assertNotIn('node_pool   = "ingress"', job)
        self.assertIn('operator = "distinct_hosts"', service)
        self.assertIn("max_parallel      = 1", service)
        self.assertIn("auto_revert       = true", service)


if __name__ == "__main__":
    unittest.main()
