import importlib.machinery
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch


path = Path(__file__).with_name("mini-m5-guest-bootstrap-cleanup")
loader = importlib.machinery.SourceFileLoader("m5_guest_bootstrap_cleanup", str(path))
spec = importlib.util.spec_from_loader(loader.name, loader)
module = importlib.util.module_from_spec(spec)
loader.exec_module(module)


class BootstrapCleanupTests(unittest.TestCase):
    def test_rejects_non_guest_before_any_effect(self):
        with patch.object(module.platform, "system", return_value="Linux"), \
                patch.object(module, "command") as command:
            with self.assertRaisesRegex(ValueError, "pinned disposable guest"):
                module.cleanup("12345", "a" * 64)
        command.assert_not_called()


if __name__ == "__main__":
    unittest.main()
