#!/usr/bin/env python3
"""Protect host-agent fencing from PID reuse after launchd bootout."""

import os
from pathlib import Path
import subprocess
import unittest


SCRIPT = Path(__file__).with_name("platform-upgrade")
TEXT = SCRIPT.read_text(encoding="utf-8")
START = "legacy_baseline_child_identity_matches() {"
END = "\n}\n\nlegacy_baseline_quiesce_launchd_service() {"
IDENTITY_FUNCTION = TEXT.split(START, 1)[1].split(END, 1)[0]
IDENTITY_FUNCTION = START + IDENTITY_FUNCTION + "\n}"


class ProcessIdentityTests(unittest.TestCase):
    def matches(self, expected: str, observed: str) -> bool:
        command = "\n".join((
            'ps() { printf "%s\\n" "$FAKE_PS_IDENTITY"; }',
            IDENTITY_FUNCTION,
            'legacy_baseline_child_identity_matches 123 "$EXPECTED_IDENTITY"',
        ))
        env = os.environ | {
            "EXPECTED_IDENTITY": expected,
            "FAKE_PS_IDENTITY": observed,
        }
        result = subprocess.run(["bash", "-c", command], env=env, check=False)
        return result.returncode == 0

    def test_original_process_identity_matches(self):
        identity = "Sat Nov  7 14:00:00 2026 /host/bin/norn-host-agent"
        self.assertTrue(self.matches(identity, identity))

    def test_reused_pid_with_another_command_does_not_match(self):
        expected = "Sat Nov  7 14:00:00 2026 /host/bin/norn-host-agent"
        observed = "Sat Nov  7 14:00:01 2026 /usr/bin/other-process"
        self.assertFalse(self.matches(expected, observed))

    def test_reused_pid_with_same_command_but_new_start_time_does_not_match(self):
        expected = "Sat Nov  7 14:00:00 2026 /host/bin/norn-host-agent"
        observed = "Sat Nov  7 14:00:01 2026 /host/bin/norn-host-agent"
        self.assertFalse(self.matches(expected, observed))

    def test_fence_signals_and_waits_for_the_captured_identity_only(self):
        body = TEXT.split("legacy_baseline_quiesce_launchd_service() {", 1)[1].split(
            "\n}\n", 1
        )[0]
        self.assertIn('ps -o lstart= -o command= -p "$child_pid"', body)
        self.assertIn('&& legacy_baseline_child_identity_matches "$child_pid" "$child_identity"; then', body)
        self.assertNotIn("child identity changed after launchd bootout", body)


if __name__ == "__main__":
    unittest.main()
