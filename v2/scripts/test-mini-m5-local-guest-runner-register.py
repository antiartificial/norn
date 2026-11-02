import importlib.machinery
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


path = Path(__file__).with_name("mini-m5-local-guest-runner-register")
loader = importlib.machinery.SourceFileLoader("m5_runner_register", str(path))
spec = importlib.util.spec_from_loader(loader.name, loader)
module = importlib.util.module_from_spec(spec)
loader.exec_module(module)


class RunnerRegisterTests(unittest.TestCase):
    def test_existing_runner_name_blocks_guest_effect(self):
        with tempfile.TemporaryDirectory() as directory:
            with patch.object(module, "verify", return_value=Path(directory)), \
                    patch.object(module, "inventory", return_value=[{"id": 1, "name": module.RUNNER_NAME}]), \
                    patch.object(module.subprocess, "run") as process:
                with self.assertRaisesRegex(ValueError, "runner name already exists"):
                    module.register({"guestName": "norn-m5-12345"}, object(), "short-lived-token")
            process.assert_not_called()
            self.assertEqual(list(Path(directory).iterdir()), [])

    def test_unprotected_registration_rejected(self):
        with patch.dict(module.os.environ, {}, clear=True):
            with self.assertRaisesRegex(ValueError, "unexpected runner registration intent"):
                module.verify({}, object())


if __name__ == "__main__":
    unittest.main()
