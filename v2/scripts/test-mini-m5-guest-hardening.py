import importlib.machinery
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from types import SimpleNamespace
from unittest.mock import patch


path = Path(__file__).with_name("mini-m5-guest-hardening")
loader = importlib.machinery.SourceFileLoader("m5_guest_hardening", str(path))
spec = importlib.util.spec_from_loader(loader.name, loader)
hardening = importlib.util.module_from_spec(spec)
loader.exec_module(hardening)


class GuestHardeningTests(unittest.TestCase):
    def test_start_creates_separate_accounts_before_one_boot_login(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            root.chmod(0o700)
            users = {hardening.REHEARSAL: SimpleNamespace(pw_uid=510, pw_dir='/Users/norn-m5-rehearsal'),
                     hardening.RUNNER: SimpleNamespace(pw_uid=511, pw_dir='/Users/norn-m5-runner')}
            with patch.object(hardening, 'ROOT', root), \
                    patch.object(hardening, 'STARTED', root / 'account-handoff-started.json'), \
                    patch.object(hardening, 'PASSWORD', root / 'rehearsal-login-password'), \
                    patch.object(hardening, 'FINISHED', root / 'account-handoff-finished.json'), \
                    patch.object(hardening, 'guest_only'), \
                    patch.object(hardening, 'account_absent'), \
                    patch.object(hardening, 'private_prompt') as prompt, \
                    patch.object(hardening, 'autologin_status', return_value='Automatic login for norn-m5-rehearsal is enabled'), \
                    patch.object(hardening.pwd, 'getpwnam', side_effect=lambda name: users[name]):
                hardening.start()
            self.assertEqual(prompt.call_count, 3)
            self.assertEqual(prompt.call_args_list[-1].args[0][1:3], ('-autologin', 'set'))
            self.assertTrue((root / 'rehearsal-login-password').is_file())

    def test_rejects_physical_mac(self):
        with patch.object(hardening.os, "geteuid", return_value=0), \
                patch.object(hardening.platform, "system", return_value="Darwin"), \
                patch.object(hardening.platform, "machine", return_value="arm64"), \
                patch.object(hardening, "command", return_value="Mac14,7\n"):
            with self.assertRaisesRegex(ValueError, "VirtualMac"):
                hardening.guest_only()

    def test_finish_proves_one_boot_login_was_removed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            root.chmod(0o700)
            (root / "account-handoff-started.json").write_text("{}\n")
            (root / "rehearsal-login-password").write_text("secret\n")
            users = {hardening.REHEARSAL: SimpleNamespace(pw_uid=510),
                     hardening.RUNNER: SimpleNamespace(pw_uid=511)}
            def command(*args, **_kwargs):
                if args[-1] == "managername":
                    return "Aqua\n"
                return ""
            with patch.object(hardening, "ROOT", root), \
                    patch.object(hardening, "STARTED", root / "account-handoff-started.json"), \
                    patch.object(hardening, "PASSWORD", root / "rehearsal-login-password"), \
                    patch.object(hardening, "FINISHED", root / "account-handoff-finished.json"), \
                    patch.object(hardening, "guest_only"), \
                    patch.object(hardening, "command", side_effect=command), \
                    patch.object(hardening, "autologin_status", return_value="Automatic login is disabled"), \
                    patch.object(hardening, "remove_new_account_keychains"), \
                    patch.object(hardening.pwd, "getpwnam", side_effect=lambda name: users[name]), \
                    patch.object(hardening.subprocess, "run", return_value=SimpleNamespace(returncode=1)), \
                    patch.object(Path, "exists", autospec=True, side_effect=lambda self: False if str(self) == "/etc/kcpassword" else self.is_file()):
                hardening.finish()
            receipt = json.loads((root / "account-handoff-finished.json").read_text())
            self.assertEqual(receipt["guiManager"], "Aqua")
            self.assertFalse((root / "rehearsal-login-password").exists())

    def test_finish_removes_only_new_accounts_keychain_files(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            keychains = home / "Library/Keychains"
            keychains.mkdir(parents=True)
            (keychains / "login.keychain-db").write_bytes(b"new disposable login keychain")
            hardening.remove_new_account_keychains(hardening.os.getuid(), home)
            self.assertFalse(keychains.exists())

    def test_finish_rejects_linked_keychain(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            keychains = home / "Library/Keychains"
            keychains.mkdir(parents=True)
            (keychains / "linked").symlink_to("/etc/passwd")
            with self.assertRaisesRegex(ValueError, "unsafe"):
                hardening.remove_new_account_keychains(hardening.os.getuid(), home)
            self.assertTrue(keychains.exists())

    def test_finish_rejects_linked_library(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory) / "home"
            external = Path(directory) / "external"
            home.mkdir()
            (external / "Keychains").mkdir(parents=True)
            (external / "Keychains/login.keychain-db").write_bytes(b"leave intact")
            (home / "Library").symlink_to(external)
            with self.assertRaisesRegex(ValueError, "Library directory is unsafe"):
                hardening.remove_new_account_keychains(hardening.os.getuid(), home)
            self.assertTrue((external / "Keychains/login.keychain-db").exists())


if __name__ == "__main__":
    unittest.main()
