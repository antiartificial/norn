import importlib.machinery
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


path = Path(__file__).with_name("mini-m5-local-guest-runner-revoke")
loader = importlib.machinery.SourceFileLoader("m5_runner_revoke", str(path))
spec = importlib.util.spec_from_loader(loader.name, loader)
module = importlib.util.module_from_spec(spec)
loader.exec_module(module)


class RunnerRevokeTests(unittest.TestCase):
    def test_exact_idle_runner_is_revoked_and_absence_receipted(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            root.chmod(0o700)
            intent = {"guestName": "norn-m5-12345", "runnerId": 42, "createMarkerSHA256": "a" * 64}
            with patch.object(module, "verify", return_value=root), \
                    patch.object(module, "inventory", side_effect=[
                        [{"name": module.RUNNER, "id": 42, "busy": False}], []]), \
                    patch.object(module, "request") as request:
                receipt = module.revoke(intent, root, "short-lived-token")
            request.assert_called_once_with("short-lived-token", "DELETE", module.API + "/42")
            self.assertTrue((root / "norn-m5-12345.runner-revoke-started").exists())
            self.assertEqual(json.loads(receipt.read_text())["runnerId"], 42)

    def test_busy_runner_has_no_effect(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            root.chmod(0o700)
            with patch.object(module, "verify", return_value=root), \
                    patch.object(module, "inventory", return_value=[
                        {"name": module.RUNNER, "id": 42, "busy": True}]), \
                    patch.object(module, "request") as request:
                with self.assertRaisesRegex(ValueError, "busy"):
                    module.revoke({"guestName": "norn-m5-12345", "runnerId": 42}, root, "token")
            request.assert_not_called()
            self.assertEqual(list(root.iterdir()), [])


if __name__ == "__main__":
    unittest.main()
