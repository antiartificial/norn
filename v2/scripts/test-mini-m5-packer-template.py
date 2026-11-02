import importlib.machinery
import importlib.util
from pathlib import Path
import unittest


path = Path(__file__).with_name("mini-m5-packer-template")
loader = importlib.machinery.SourceFileLoader("m5_packer_template", str(path))
spec = importlib.util.spec_from_loader(loader.name, loader)
template = importlib.util.module_from_spec(spec)
loader.exec_module(template)


class TemplateTests(unittest.TestCase):
    def test_restricted_derivation(self):
        result = template.transform(template.SOURCE.read_bytes()).decode()
        self.assertIn('run_extra_args = ["--net-softnet"]', result)
        self.assertIn('from_ipsw    = var.m5_ipsw_path', result)
        self.assertIn('version = "= 1.21.0"', result)
        self.assertIn('${var.m5_bootstrap_password}', result)
        self.assertIn('source      = var.m5_guest_prepare_path', result)
        self.assertIn('source      = var.m5_window_helper_path', result)
        self.assertIn('source      = var.m5_checker_helper_path', result)
        self.assertIn('sudo -S /usr/bin/python3 /tmp/norn-m5-guest-prepare', result)
        self.assertIn('content     = var.m5_bootstrap_password', result)
        self.assertNotIn("printf '%s", result)
        self.assertNotIn('admin ALL=(ALL)', result)
        self.assertNotIn('github.com/hashicorp/ansible', result)
        self.assertNotIn('spctl --global-disable', result)
        self.assertNotIn('autoLoginUser', result)

    def test_unreviewed_upstream_rejected(self):
        with self.assertRaises(ValueError):
            template.transform(template.SOURCE.read_bytes() + b' ')


if __name__ == "__main__":
    unittest.main()
