import importlib.machinery
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from types import SimpleNamespace


path = Path(__file__).with_name("mini-m5-guest-runner-setup")
loader = importlib.machinery.SourceFileLoader("m5_guest_runner_setup", str(path))
spec = importlib.util.spec_from_loader(loader.name, loader)
module = importlib.util.module_from_spec(spec)
loader.exec_module(module)


class RunnerSetupTests(unittest.TestCase):
    def test_registration_token_is_not_in_process_arguments(self):
        user = SimpleNamespace(pw_gid=501, pw_uid=501, pw_dir="/Users/norn-m5-runner")
        token = "sensitive-registration-token"
        with patch.object(module.subprocess, "run", return_value=SimpleNamespace(returncode=0)) as process:
            module.run_as_runner(("/tmp/config.sh", "--unattended"), user, token=token)
        args, kwargs = process.call_args
        self.assertNotIn(token, args[0])
        self.assertEqual(kwargs["env"]["ACTIONS_RUNNER_INPUT_TOKEN"], token)
        self.assertNotIn("input", kwargs)

    def test_rejects_non_guest_without_install_effect(self):
        with tempfile.TemporaryDirectory() as directory:
            archive = Path(directory) / "runner.tgz"
            archive.write_bytes(b"not the pinned runner")
            with patch.object(module.platform, "system", return_value="Linux"), \
                    patch.object(module.subprocess, "run") as process:
                with self.assertRaisesRegex(ValueError, "guest root"):
                    module.install(archive, "A" * 25)
            process.assert_not_called()

if __name__ == "__main__":
    unittest.main()
