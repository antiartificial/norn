import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import shutil
import tempfile
import unittest
from unittest.mock import patch


path = Path(__file__).with_name("mini-m5-local-guest-controller")
loader = importlib.machinery.SourceFileLoader("m5_local_guest", str(path))
spec = importlib.util.spec_from_loader(loader.name, loader)
controller = importlib.util.module_from_spec(spec)
loader.exec_module(controller)


class ReservationTests(unittest.TestCase):
    def test_tart_finds_pinned_softnet_command_by_name(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            root.chmod(0o700)
            softnet = root / "norn-m5-softnet"
            softnet.write_text("#!/bin/sh\nexit 0\n")
            softnet.chmod(0o4755)
            with patch.object(controller, "checked_file"):
                command_dir = controller.softnet_command_directory(root, "norn-m5-12345", softnet)
            command = command_dir / "softnet"
            self.assertEqual(command_dir.stat().st_mode & 0o777, 0o700)
            self.assertEqual(shutil.which("softnet", path=str(command_dir)), str(command))
            self.assertTrue(os.path.samefile(command, softnet))
            with patch.object(controller, "checked_file"):
                with self.assertRaises(FileExistsError):
                    controller.softnet_command_directory(root, "norn-m5-12345", softnet)

    def test_create_requires_exact_protected_runner_context(self):
        with patch.dict(controller.os.environ, {}, clear=True):
            with self.assertRaisesRegex(ValueError, "exact protected supervisor"):
                controller.protected_run({"runId": "12345", "sourceSHA": "a" * 40})

    def test_reconcile_reports_reserved_without_guest_effect(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            root.chmod(0o700)
            intent = {"guestName": "norn-m5-12345", "runId": "12345", "sourceSHA": "a" * 40}
            controller.reserve(root, intent, {})
            with patch.object(controller, "protected_run"), patch.object(controller, "tart_inventory") as inventory:
                result = controller.reconcile(root, intent, "/bin/false")
            self.assertEqual(result["state"], "reserved-no-effect")
            inventory.assert_not_called()

    def test_one_use_owner_only_journal(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            root.chmod(0o700)
            intent = {"guestName": "norn-m5-12345", "runId": "12345"}
            evidence = {"schema": "norn.m5-local-guest-preflight/v1", "runId": "12345"}
            journal = controller.reserve(root, intent, evidence)
            self.assertEqual(journal.stat().st_mode & 0o777, 0o600)
            self.assertEqual(json.loads(journal.read_text())["state"], "create-reserved")
            with self.assertRaises(FileExistsError):
                controller.reserve(root, intent, evidence)

    def test_mutable_journal_root_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            root.chmod(0o755)
            with self.assertRaises(ValueError):
                controller.reserve(root, {"guestName": "norn-m5-12345"}, {})

    def test_ambiguous_tart_inventory_rejected(self):
        with patch.object(controller.subprocess, "run") as run:
            run.return_value.stdout = '[{"name":"norn-m5-12345"}]'
            with self.assertRaises(ValueError):
                controller.tart_inventory("/bin/false")


if __name__ == "__main__":
    unittest.main()
