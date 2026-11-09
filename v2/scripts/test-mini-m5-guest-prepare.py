import importlib.machinery
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch


path = Path(__file__).with_name("mini-m5-guest-prepare")
loader = importlib.machinery.SourceFileLoader("m5_guest_prepare", str(path))
spec = importlib.util.spec_from_loader(loader.name, loader)
prepare = importlib.util.module_from_spec(spec)
loader.exec_module(prepare)


class GuestPrepareTests(unittest.TestCase):
    def test_stops_loaded_screen_sharing_service(self):
        with patch.object(prepare, "command") as command, \
                patch.object(prepare.subprocess, "run") as run:
            run.return_value.returncode = 0
            prepare.stop_screen_sharing()
            command.assert_any_call("/bin/launchctl", "disable", "system/com.apple.screensharing")
            run.assert_called_once_with(("/bin/launchctl", "bootout", "system/com.apple.screensharing"),
                                        capture_output=True, text=True, timeout=20)

    def test_absent_screen_sharing_service_is_already_stopped(self):
        with patch.object(prepare, "command") as command, \
                patch.object(prepare.subprocess, "run") as run:
            run.return_value.returncode = 113
            run.return_value.stderr = "Could not find service"
            prepare.stop_screen_sharing()

    def test_unexpected_screen_sharing_bootout_error_fails_closed(self):
        with patch.object(prepare, "command"), patch.object(prepare.subprocess, "run") as run:
            run.return_value.returncode = 5
            run.return_value.stderr = "permission denied"
            with self.assertRaisesRegex(ValueError, "could not be stopped"):
                prepare.stop_screen_sharing()

    def test_inserts_filter_anchor_after_translation(self):
        original = ('scrub-anchor "com.apple/*"\n'
                    'nat-anchor "com.apple/*"\n'
                    'rdr-anchor "com.apple/*"\n'
                    'dummynet-anchor "com.apple/*"\n'
                    'anchor "com.apple/*"\n')
        result = prepare.pf_with_anchor(original)
        self.assertIn('dummynet-anchor "com.apple/*"\nanchor "com.norn.m5.historical"\n', result)
        self.assertEqual(result.count('anchor "com.norn.m5.historical"'), 1)
        with self.assertRaisesRegex(ValueError, "already present"):
            prepare.pf_with_anchor(result)
        with self.assertRaisesRegex(ValueError, "precedes"):
            prepare.pf_with_anchor(original.replace('\nanchor "com.apple/*"\n',
                                                    '\npass out all\nanchor "com.apple/*"\n'))

    def test_requires_virtual_mac_and_exact_os(self):
        with patch.object(prepare.os, "geteuid", return_value=0), \
                patch.object(prepare.platform, "system", return_value="Darwin"), \
                patch.object(prepare.platform, "machine", return_value="arm64"), \
                patch.object(prepare, "command", side_effect=["Mac14,7\n", "26.6.2\n"]):
            with self.assertRaisesRegex(ValueError, "pinned disposable"):
                prepare.guest_only()
        with patch.object(prepare.os, "geteuid", return_value=0), \
                patch.object(prepare.platform, "system", return_value="Darwin"), \
                patch.object(prepare.platform, "machine", return_value="arm64"), \
                patch.object(prepare, "command", side_effect=["VirtualMac2,1\n", "26.6.2\n"]):
            prepare.guest_only()


if __name__ == "__main__":
    unittest.main()
