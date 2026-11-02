import importlib.machinery
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


path = Path(__file__).with_name("mini-m5-local-guest-harden")
loader = importlib.machinery.SourceFileLoader("m5_local_guest_harden", str(path))
spec = importlib.util.spec_from_loader(loader.name, loader)
module = importlib.util.module_from_spec(spec)
loader.exec_module(module)


class HardenTests(unittest.TestCase):
    def test_unadmitted_guest_cannot_start(self):
        class Args:
            journal_root = ""

        with tempfile.TemporaryDirectory() as directory:
            Args.journal_root = directory
            with patch.object(module, "admission", side_effect=ValueError("unreviewed Tart guest")), \
                    patch.object(module.subprocess, "run") as process:
                with self.assertRaisesRegex(ValueError, "unreviewed Tart guest"):
                    module.harden(Args())
            process.assert_not_called()
            self.assertEqual(list(Path(directory).iterdir()), [])


if __name__ == "__main__":
    unittest.main()
