#!/usr/bin/env python3
"""Exercise the real host assurance script against disposable loopback services."""

import http.server
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
import unittest


SCRIPT = Path(__file__).with_name("host-runtime")


class RuntimeHandler(http.server.BaseHTTPRequestHandler):
    fence_state = "active"

    def do_GET(self):
        if self.path == "/api/health":
            payload = {"status": "ok", "runtimeMutationFence": self.fence_state}
        elif self.path == "/api/apps":
            payload = []
        elif self.path == "/v1/nodes":
            payload = [{"Status": "ready", "Attributes": {"unique.network.ip-address": "127.0.0.1"}}]
        elif self.path == "/v1/status/leader":
            payload = "127.0.0.1"
        else:
            self.send_error(404)
            return
        body = json.dumps(payload).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        if self.path != "/api/events":
            self.send_error(404)
            return
        self.send_response(204)
        self.end_headers()

    def log_message(self, *args):
        pass


class HostAssuranceRouteFenceTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = http.server.ThreadingHTTPServer(("127.0.0.1", 8800), RuntimeHandler)
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.server.server_close()
        cls.thread.join()

    def test_forge_route_reconciliation_obeys_runtime_fence(self):
        with tempfile.TemporaryDirectory(prefix="norn-assure-routes-") as root:
            root = Path(root)
            config = root / "config"
            (config / "bin").mkdir(parents=True)
            (config / "assure-forge").write_text("example-app\n")
            norn = config / "bin" / "norn"
            norn.write_text('#!/bin/sh\nprintf "%s\\n" "$*" >> "$CALL_LOG"\n')
            norn.chmod(0o700)
            platform = config / "bin" / "platform-upgrade"
            platform.write_text('#!/bin/sh\n[ "$1" = env-exec ] && [ "$2" = -- ] || exit 90\nshift 2\nexec "$@"\n')
            platform.chmod(0o700)
            runtime = config / "bin" / "host-runtime"
            runtime.write_bytes(SCRIPT.read_bytes())
            runtime.chmod(0o700)
            calls = root / "calls"
            env = {**os.environ, "NORN_URL": "http://127.0.0.1:8800", "NOMAD_ADDR": "http://127.0.0.1:8800",
                   "CONSUL_HTTP_ADDR": "http://127.0.0.1:8800", "CALL_LOG": str(calls)}
            for state, allowed in (("active", False), ("unknown", False), ("inactive", True)):
                with self.subTest(state=state):
                    RuntimeHandler.fence_state = state
                    calls.unlink(missing_ok=True)
                    command = [str(SCRIPT), "assure", "--config-dir", str(config), "--state-dir", str(root / "state"),
                               "--address", "127.0.0.1"]
                    result = subprocess.run(command, env=env, capture_output=True, text=True, timeout=20)
                    self.assertEqual(result.returncode == 0, allowed, result.stdout + result.stderr)
                    self.assertEqual(calls.read_text().strip() if calls.exists() else "", "forge example-app" if allowed else "")
                    if not allowed:
                        self.assertIn("runtime mutation fence is active or unproven", result.stdout)

    def test_authenticated_forge_child_is_allowlist_bound(self):
        with tempfile.TemporaryDirectory(prefix="norn-assure-forge-child-") as root:
            root = Path(root)
            config = root / "config"
            (config / "bin").mkdir(parents=True)
            (config / "assure-forge").write_text("example-app\n")
            calls = root / "calls"
            norn = config / "bin" / "norn"
            norn.write_text('#!/bin/sh\nprintf "%s|%s\\n" "$NORN_PLATFORM_REPO" "$*" >> "$CALL_LOG"\n')
            norn.chmod(0o700)
            env = {**os.environ, "NORN_PLATFORM_REPO": str(root / "repo"),
                   "NORN_HOST_CONFIG_DIR": str(config), "CALL_LOG": str(calls)}
            allowed = subprocess.run([str(SCRIPT), "_assurance-forge", "example-app"],
                                     env=env, capture_output=True, text=True, timeout=10)
            self.assertEqual(allowed.returncode, 0, allowed.stderr)
            self.assertEqual(calls.read_text().strip(),
                             f"{root / 'repo'}|forge example-app")

            calls.unlink()
            denied = subprocess.run([str(SCRIPT), "_assurance-forge", "other-app"],
                                    env=env, capture_output=True, text=True, timeout=10)
            self.assertNotEqual(denied.returncode, 0)
            self.assertIn("not in the assurance forge allowlist", denied.stderr)
            self.assertFalse(calls.exists())

            extra = subprocess.run([str(SCRIPT), "_assurance-forge", "example-app", "extra"],
                                   env=env, capture_output=True, text=True, timeout=10)
            self.assertNotEqual(extra.returncode, 0)


if __name__ == "__main__":
    unittest.main()
