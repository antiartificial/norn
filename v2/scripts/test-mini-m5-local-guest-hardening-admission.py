import importlib.machinery
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch


path = Path(__file__).with_name("mini-m5-local-guest-hardening-admission")
loader = importlib.machinery.SourceFileLoader("m5_hardening_admission", str(path))
spec = importlib.util.spec_from_loader(loader.name, loader)
admission = importlib.util.module_from_spec(spec)
loader.exec_module(admission)


class HardeningAdmissionTests(unittest.TestCase):
    def test_protected_run_is_exact(self):
        intent = {"runId": "67890", "sourceSHA": "a" * 40}
        with patch.dict(admission.os.environ, {}, clear=True):
            with self.assertRaisesRegex(ValueError, "exact protected supervisor"):
                admission.protected(intent)
        expected = {"GITHUB_ACTIONS": "true", "GITHUB_REF": "refs/heads/master",
                    "GITHUB_REF_PROTECTED": "true", "RUNNER_NAME": "norn-m5-supervisor-01",
                    "GITHUB_RUN_ID": "67890", "GITHUB_SHA": "a" * 40,
                    "NORN_M5_PROTECTED_CONTROLLER": "1"}
        with patch.dict(admission.os.environ, expected, clear=True):
            admission.protected(intent)

    def test_intent_requires_all_guest_identity_hashes(self):
        with self.assertRaisesRegex(ValueError, "unexpected hardening intent"):
            admission.verify({"schema": "norn.m5-local-guest-hardening-intent/v1"},
                             "/tmp/journal", "/tmp/tart", "/tmp/runner.pub")


if __name__ == "__main__":
    unittest.main()
