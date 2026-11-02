#!/usr/bin/env python3
"""Check the protected runner's firewall attestation contract."""

import importlib.machinery
import importlib.util
from pathlib import Path
from unittest import TestCase, main, mock
import json
from types import SimpleNamespace
import subprocess


path = Path(__file__).with_name("mini-m5-isolation-check")
loader = importlib.machinery.SourceFileLoader("m5_isolation", str(path))
spec = importlib.util.spec_from_loader(loader.name, loader)
checker = importlib.util.module_from_spec(spec)
loader.exec_module(checker)


class IsolationCheckTest(TestCase):
    rules = """pass out quick on lo0 all
block drop out quick all
"""

    def run_checker(self, rules=None, root_rules='anchor "com.norn.m5.historical" all\n', gui_available=True):
        commands = {
            ('/bin/launchctl', 'print', 'gui/510'): 'gui/510 = { state = running }\n',
            ('/sbin/pfctl', '-s', 'info'): 'Status: Enabled\n',
            ('/sbin/pfctl', '-sr'): root_rules,
            ('/sbin/pfctl', '-a', checker.ANCHOR, '-sr'): self.rules if rules is None else rules,
            ('/usr/sbin/netstat', '-rn', '-f', 'inet'): 'Destination Gateway Flags Netif\ndefault 203.0.113.1 UGSc en0\n',
        }
        users = {
            checker.REHEARSAL_USER: SimpleNamespace(pw_uid=510, pw_dir='/Users/norn-m5-rehearsal'),
            checker.RUNNER_USER: SimpleNamespace(pw_uid=511, pw_dir='/Users/norn-m5-runner'),
        }
        def command(*args):
            if args[0] == '/bin/launchctl' and not gui_available:
                raise subprocess.CalledProcessError(113, args)
            return commands[args]
        with (mock.patch.object(checker.os, 'geteuid', return_value=0),
              mock.patch.object(checker.sys, 'platform', 'darwin'),
              mock.patch.object(checker.platform, 'machine', return_value='arm64'),
              mock.patch.object(checker.sys, 'argv', [str(path), checker.RUNNER]),
              mock.patch.object(checker, 'command', side_effect=command),
              mock.patch.object(checker.pwd, 'getpwnam', side_effect=lambda name: users[name]),
              mock.patch.object(checker.os.path, 'lexists', return_value=False),
              mock.patch.object(checker.subprocess, 'run', return_value=SimpleNamespace(returncode=1, stdout='')),
              mock.patch('builtins.print') as output):
            checker.main()
        return json.loads(output.call_args.args[0])

    def test_attests_attached_deny_rules(self):
        result = self.run_checker()
        self.assertEqual(result['hostOutbound'], 'loopback-only')
        self.assertTrue(result['pfEnabled'])

    def test_rejects_earlier_public_pass(self):
        with self.assertRaisesRegex(SystemExit, 'loopback-only pass'):
            self.run_checker(rules='pass out quick all\n' + self.rules)

    def test_rejects_ipv4_only_block(self):
        with self.assertRaisesRegex(SystemExit, 'deny-all'):
            self.run_checker(rules=self.rules.replace('block drop out quick all', 'block drop out quick inet all'))

    def test_rejects_unattached_anchor(self):
        with self.assertRaisesRegex(SystemExit, 'not the first'):
            self.run_checker(root_rules='pass out all\n')

    def test_rejects_missing_gui_launchd_domain(self):
        with self.assertRaises(subprocess.CalledProcessError):
            self.run_checker(gui_available=False)


if __name__ == '__main__':
    main()
