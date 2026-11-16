#!/usr/bin/env python3
"""Keep a platform source tag from publishing an unrelated application."""
import fnmatch
import json
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[2]


class ReleaseTagBoundaries(unittest.TestCase):
    def test_platform_tags_do_not_select_the_sample_application(self):
        workflow = (ROOT / '.github/workflows/hello-norn-mysql-release.yml').read_text()
        trigger = re.search(r'^on:\n  push:\n    tags: (\[[^\n]+\])$', workflow, re.M)
        self.assertIsNotNone(trigger, 'review the actual tag trigger if its YAML shape changes')
        patterns = json.loads(trigger.group(1))
        for tag in ('v3.0.0', 'v3.0.1', 'v3.0.0-control', 'v3.0.0-platform',
                    'v3.0.0-rc.1-control', 'platform-' + 'a' * 40):
            with self.subTest(tag=tag):
                self.assertFalse(any(fnmatch.fnmatchcase(tag, pattern) for pattern in patterns))
        self.assertTrue(any(fnmatch.fnmatchcase('hello-norn-mysql-v1.0.0', pattern)
                            for pattern in patterns))

    def test_production_job_also_requires_the_application_namespace(self):
        workflow = (ROOT / '.github/workflows/hello-norn-mysql-release.yml').read_text()
        job = workflow.split('\n  production:\n', 1)[1]
        condition = re.search(r'^    if: (.+)$', job, re.M).group(1)
        self.assertIn("github.event_name == 'push'", condition)
        self.assertIn("startsWith(github.ref, 'refs/tags/hello-norn-mysql-v')", condition)
        self.assertIn('github.ref_protected', condition)

    def test_platform_publisher_still_requires_explicit_dispatch(self):
        workflow = (ROOT / '.github/workflows/platform-release.yml').read_text()
        triggers = workflow.split('\non:\n', 1)[1].split('\npermissions:', 1)[0]
        self.assertIn('  workflow_dispatch:', triggers)
        self.assertNotRegex(triggers, re.compile(r'^  (push|release):', re.M))


if __name__ == '__main__':
    unittest.main()
