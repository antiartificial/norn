#!/usr/bin/env python3
"""Check the protected runner's firewall attestation contract."""

import importlib.machinery
import importlib.util
from pathlib import Path
from unittest import TestCase, main, mock
import json


path = Path(__file__).with_name("mini-m5-isolation-check")
loader = importlib.machinery.SourceFileLoader("m5_isolation", str(path))
spec = importlib.util.spec_from_loader(loader.name, loader)
checker = importlib.util.module_from_spec(spec)
loader.exec_module(checker)


class IsolationCheckTest(TestCase):
    rules = """block drop out quick inet from any to 10.0.0.0/8
block drop out quick inet from any to 172.16.0.0/12
block drop out quick inet from any to 192.168.0.0/16
block drop out quick inet from any to 100.64.0.0/10
block drop out quick inet6 from any to fd00::/8
"""

    def run_checker(self, rules=None, root_rules='anchor "com.norn.m5.historical" all\n'):
        commands = {
            ('/sbin/pfctl', '-s', 'info'): 'Status: Enabled\n',
            ('/sbin/pfctl', '-sr'): root_rules,
            ('/sbin/pfctl', '-a', checker.ANCHOR, '-sr'): self.rules if rules is None else rules,
            ('/usr/sbin/netstat', '-rn', '-f', 'inet'): 'Destination Gateway Flags Netif\ndefault 203.0.113.1 UGSc en0\n',
        }
        with (mock.patch.object(checker.os, 'geteuid', return_value=0),
              mock.patch.object(checker.sys, 'platform', 'darwin'),
              mock.patch.object(checker.platform, 'machine', return_value='arm64'),
              mock.patch.object(checker.sys, 'argv', [str(path), checker.RUNNER]),
              mock.patch.object(checker, 'command', side_effect=lambda *args: commands[args]),
              mock.patch('builtins.print') as output):
            checker.main()
        return json.loads(output.call_args.args[0])

    def test_attests_attached_deny_rules(self):
        result = self.run_checker()
        self.assertEqual(result['deniedNetworks'], list(checker.DENIED))
        self.assertTrue(result['pfEnabled'])

    def test_rejects_missing_private_network_rule(self):
        with self.assertRaisesRegex(SystemExit, 'missing M5 private-network deny rule'):
            self.run_checker(rules=self.rules.replace('block drop out quick inet from any to 100.64.0.0/10\n', ''))

    def test_rejects_unattached_anchor(self):
        with self.assertRaisesRegex(SystemExit, 'not attached'):
            self.run_checker(root_rules='pass out all\n')


if __name__ == '__main__':
    main()
