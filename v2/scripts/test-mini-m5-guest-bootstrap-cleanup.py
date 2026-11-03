import importlib.machinery
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch
import tempfile


path = Path(__file__).with_name("mini-m5-guest-bootstrap-cleanup")
loader = importlib.machinery.SourceFileLoader("m5_guest_bootstrap_cleanup", str(path))
spec = importlib.util.spec_from_loader(loader.name, loader)
module = importlib.util.module_from_spec(spec)
loader.exec_module(module)


class BootstrapCleanupTests(unittest.TestCase):
    def test_retires_only_recorded_home_tree(self):
        with tempfile.TemporaryDirectory() as base:
            home = Path(base) / "admin"
            home.mkdir()
            (home / "nested").mkdir()
            (home / "nested" / "file").write_text("temporary")
            outside = Path(base) / "outside"
            outside.write_text("preserve")
            (home / "link").symlink_to(outside)
            info = home.stat()
            recorded = {"homeDevice": info.st_dev, "homeInode": info.st_ino, "homeUID": info.st_uid}
            module.retire_home(home, recorded)
            self.assertFalse(home.exists())
            self.assertEqual(outside.read_text(), "preserve")

    def test_rejects_replaced_home(self):
        with tempfile.TemporaryDirectory() as base:
            home = Path(base) / "admin"
            home.mkdir()
            info = home.stat()
            recorded = {"homeDevice": info.st_dev, "homeInode": info.st_ino + 1, "homeUID": info.st_uid}
            with self.assertRaisesRegex(ValueError, "changed"):
                module.retire_home(home, recorded)
            self.assertTrue(home.exists())

    def test_rejects_non_guest_before_any_effect(self):
        with patch.object(module.platform, "system", return_value="Linux"), \
                patch.object(module, "command") as command:
            with self.assertRaisesRegex(ValueError, "pinned disposable guest"):
                module.cleanup("12345", "a" * 64)
        command.assert_not_called()


if __name__ == "__main__":
    unittest.main()
