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
