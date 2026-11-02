import importlib.machinery
import importlib.util
from datetime import datetime, timezone
from pathlib import Path
import unittest


path = Path(__file__).with_name("mini-m5-host-admission")
loader = importlib.machinery.SourceFileLoader("m5_host_admission", str(path))
spec = importlib.util.spec_from_loader(loader.name, loader)
admission = importlib.util.module_from_spec(spec)
loader.exec_module(admission)


class AdmissionTests(unittest.TestCase):
    def setUp(self):
        self.now = datetime(2026, 11, 2, 12, tzinfo=timezone.utc)
        common = {"provider": "aws", "account": "123456789012", "region": "us-east-1"}
        self.intent = {"schema": "norn.m5-dedicated-host-intent/v1", "runId": "12345",
                       "sourceSHA": "a" * 40, **common, "hostType": "mac2.metal",
                       "imageId": "ami-example", "runTag": "norn:m5:12345",
                       "sshHostKeySHA256": "b" * 64, "costCapUSD": "40.00",
                       "retireBy": "2026-11-04T12:00:00Z"}
        self.quote = {"schema": "norn.m5-dedicated-host-quote/v1", **common,
                      "hostType": "mac2.metal", "imageId": "ami-example", "currency": "USD",
                      "hourlyUSD": "1.00", "minimumHours": 24, "fixedUSD": "2.00",
                      "observedAt": "2026-11-02T11:59:00Z"}
        self.inventory = {"schema": "norn.m5-dedicated-host-inventory/v1", **common,
                          "runTag": "norn:m5:12345", "resources": [],
                          "observedAt": "2026-11-02T11:59:00Z"}

    def check(self):
        return admission.verify(self.intent, self.quote, self.inventory,
                                "12345", "a" * 40, self.now)

    def test_exact_admission(self):
        self.assertEqual(self.check()["minimumCostUSD"], "26.00")

    def test_scope_or_resource_mismatch(self):
        self.inventory["resources"] = [{"id": "existing"}]
        with self.assertRaises(ValueError):
            self.check()
        self.inventory["resources"] = []
        self.quote["region"] = "us-west-2"
        with self.assertRaises(ValueError):
            self.check()

    def test_cost_staleness_and_expiration(self):
        self.intent["costCapUSD"] = "25.99"
        with self.assertRaises(ValueError):
            self.check()
        self.intent["costCapUSD"] = "40.00"
        self.inventory["observedAt"] = "2026-11-02T11:54:59Z"
        with self.assertRaises(ValueError):
            self.check()
        self.inventory["observedAt"] = "2026-11-02T11:59:00Z"
        self.intent["retireBy"] = "2026-11-02T12:00:00Z"
        with self.assertRaises(ValueError):
            self.check()

    def test_unexpected_field_and_boolean_hours(self):
        self.quote["extra"] = "untrusted"
        with self.assertRaises(ValueError):
            self.check()
        del self.quote["extra"]
        self.quote["minimumHours"] = True
        with self.assertRaises(ValueError):
            self.check()


if __name__ == "__main__":
    unittest.main()
