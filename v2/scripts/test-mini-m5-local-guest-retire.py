import importlib.machinery
import importlib.util
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


path = Path(__file__).with_name("mini-m5-local-guest-retire")
loader = importlib.machinery.SourceFileLoader("m5_retire", str(path))
spec = importlib.util.spec_from_loader(loader.name, loader)
retire = importlib.util.module_from_spec(spec)
loader.exec_module(retire)


class RetireTests(unittest.TestCase):
    def test_retire_exact_guest_and_prove_absence(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            root.chmod(0o700)
            intent = {"guestName": "norn-m5-12345", "runId": "67890", "createMarkerSHA256": "a" * 64,
                      "tartConfigSHA256": hashlib.sha256(b"cfg").hexdigest(), "runnerState": "never-registered"}
            with patch.object(retire, "verify"), patch.object(retire, "inventory", side_effect=[
                    [{"Name": "other"}, {"Name": "norn-m5-12345", "Running": True}],
                    [{"Name": "other"}]]), patch.object(retire.subprocess, "run") as run:
                run.return_value.stdout = b"cfg"
                receipt = retire.retire(intent, root, "/pinned/tart")
            self.assertEqual(json.loads(receipt.read_text())["after"], [])
            self.assertEqual(run.call_count, 3)
            self.assertEqual(run.call_args_list[1].args[0], ("/pinned/tart", "stop", "norn-m5-12345"))
            self.assertEqual(run.call_args_list[2].args[0], ("/pinned/tart", "delete", "norn-m5-12345"))
            with patch.object(retire, "verify"), patch.object(retire, "inventory", return_value=[]):
                with self.assertRaises(FileExistsError):
                    retire.retire(intent, root, "/pinned/tart")

    def test_duplicate_identity_has_no_effect(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            root.chmod(0o700)
            intent = {"guestName": "norn-m5-12345", "runnerState": "never-registered"}
            with patch.object(retire, "verify"), patch.object(retire, "inventory", return_value=[
                    {"Name": "norn-m5-12345"}, {"Name": "norn-m5-12345"}]), \
                    patch.object(retire.subprocess, "run") as run:
                with self.assertRaisesRegex(ValueError, "duplicate"):
                    retire.retire(intent, root, "/pinned/tart")
            run.assert_not_called()

    def test_registered_runner_blocks_delete(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            root.chmod(0o700)
            (root / "norn-m5-12345.runner-registered").write_text("registered\n")
            with patch.object(retire, "verify"), patch.object(retire, "inventory") as inventory:
                with self.assertRaisesRegex(ValueError, "requires protected revocation"):
                    retire.retire({"guestName": "norn-m5-12345", "runnerState": "never-registered"}, root, "/pinned/tart")
            inventory.assert_not_called()

    def test_revoked_runner_requires_live_absence(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            root.chmod(0o700)
            name = "norn-m5-12345"
            marker = {"schema": "norn.m5-local-guest-runner-registered/v1", "guestName": name,
                      "runnerId": 42, "runnerName": "norn-m5-isolated-01"}
            (root / (name + ".runner-registered")).write_text(json.dumps(marker))
            receipt = {"schema": "norn.m5-local-guest-runner-revoked/v1", "guestName": name,
                       "runnerId": 42, "runnerName": "norn-m5-isolated-01",
                       "createMarkerSHA256": "a" * 64}
            receipt_path = root / (name + ".runner-revoked.json")
            receipt_path.write_text(json.dumps(receipt))
            intent = {"guestName": name, "runnerState": "revoked",
                      "runnerRevokedReceiptSHA256": hashlib.sha256(receipt_path.read_bytes()).hexdigest(),
                      "createMarkerSHA256": "a" * 64}
            with patch.object(retire, "verify"), patch.dict(retire.os.environ,
                    {"M5_GITHUB_ADMIN_TOKEN": "short-lived-token"}), \
                    patch.object(retire, "runner_absent", return_value=False), \
                    patch.object(retire, "inventory") as inventory:
                with self.assertRaisesRegex(ValueError, "not been proven absent"):
                    retire.retire(intent, root, "/pinned/tart")
            inventory.assert_not_called()

    def test_replacement_guest_config_blocks_delete(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            root.chmod(0o700)
            intent = {"guestName": "norn-m5-12345", "tartConfigSHA256": "a" * 64, "runnerState": "never-registered"}
            with patch.object(retire, "verify"), patch.object(retire, "inventory", return_value=[
                    {"Name": "norn-m5-12345", "Running": False}]), \
                    patch.object(retire.subprocess, "run") as run:
                run.return_value.stdout = b"replacement"
                with self.assertRaisesRegex(ValueError, "differs from signed identity"):
                    retire.retire(intent, root, "/pinned/tart")
            self.assertEqual(run.call_count, 1)


if __name__ == "__main__":
    unittest.main()
