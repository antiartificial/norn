#!/usr/bin/env python3
"""One-use firewall claim and close behavior."""

import importlib.machinery
import importlib.util
from pathlib import Path
import tempfile
from types import SimpleNamespace
from unittest import TestCase, main, mock


path = Path(__file__).with_name("mini-m5-firewall-window")
loader = importlib.machinery.SourceFileLoader("m5_window", str(path))
spec = importlib.util.spec_from_loader(loader.name, loader)
window = importlib.util.module_from_spec(spec)
loader.exec_module(window)


class FirewallWindowTest(TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        state = Path(self.temp.name)
        state.chmod(0o700)
        original_stat = Path.stat
        original_fstat = window.os.fstat
        def state_stat(target, *args, **kwargs):
            result = original_stat(target, *args, **kwargs)
            if target == state:
                return SimpleNamespace(st_uid=0, st_mode=result.st_mode)
            return result
        def claim_fstat(fd):
            result = original_fstat(fd)
            return SimpleNamespace(st_uid=0, st_mode=result.st_mode)
        patchers = (
            mock.patch.object(Path, 'stat', state_stat),
            mock.patch.object(window.os, 'fstat', claim_fstat),
            mock.patch.object(window, 'STATE_DIR', state),
            mock.patch.object(window, 'CLAIM', state / 'claim.json'),
            mock.patch.object(window.os, 'geteuid', return_value=0),
            mock.patch.object(window, 'pf'),
            mock.patch.object(window, 'boot_id', return_value='123'),
            mock.patch.object(window.subprocess, 'run', return_value=SimpleNamespace(stdout='')),
        )
        for patcher in patchers:
            patcher.start()
            self.addCleanup(patcher.stop)

    def run_action(self, action, run_id='41387901422'):
        with mock.patch.object(window.sys, 'argv', [str(path), action, run_id]):
            window.main()

    def test_one_use_enable_and_exact_disable(self):
        self.run_action('enable')
        self.assertTrue(window.CLAIM.is_file())
        self.assertEqual(window.pf.call_args_list[:2], [
            mock.call('-a', window.ANCHOR, '-f', str(window.STATE_DIR / 'rules-41387901422')),
            mock.call('-F', 'states'),
        ])
        with self.assertRaisesRegex(SystemExit, 'protected boot reconciliation'):
            self.run_action('enable')
        with self.assertRaisesRegex(SystemExit, 'does not match'):
            self.run_action('disable', '41387901423')
        self.assertTrue(window.CLAIM.is_file())
        self.run_action('disable')
        self.assertFalse(window.CLAIM.exists())

    def test_invalid_run_id_never_opens_window(self):
        with self.assertRaisesRegex(SystemExit, 'invalid protected run id'):
            self.run_action('enable', 'bad')
        self.assertFalse(window.CLAIM.exists())

    def test_boot_claim_blocks_new_admission_and_cross_boot_disable(self):
        self.run_action('enable')
        with self.assertRaisesRegex(SystemExit, 'protected boot reconciliation'):
            self.run_action('admit')
        with mock.patch.object(window, 'boot_id', return_value='456'):
            with self.assertRaisesRegex(SystemExit, 'does not match'):
                self.run_action('disable')
        self.assertTrue(window.CLAIM.exists())


if __name__ == '__main__':
    main()
