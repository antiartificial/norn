#!/usr/bin/env python3
import hashlib
import json
import os
import subprocess
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

SCRIPT = Path(__file__).with_name("mini-protected-transition-rehearsal")
LEGACY = "a" * 40
CANDIDATE = "b" * 40


class Handler(BaseHTTPRequestHandler):
    state = {
        "/api/health": {"status": "ok", "services": {"nomad": "up", "consul": "up",
            "workload-connector/nomad-consul": "up"}, "network": {"workloadConnector": "nomad-consul"}},
        "/api/apps": [{"spec": {"name": "fixture", "deploy": True, "endpoints": [{"url": "https://fixture.example"}],
                                  "secrets": ["DATABASE_URL"], "processes": {"cron": {"schedule": "0 * * * *"}}},
                       "allocations": [{"id": "alloc-1"}]}],
        "/api/services/manifest": {"generatedAt": "ignored", "services": [
            {"name": "fixture-cron", "app": "fixture", "process": "cron", "type": "cron"}]},
    }

    def do_GET(self):
        body = json.dumps(self.state[self.path]).encode()
        self.send_response(200); self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body))); self.end_headers(); self.wfile.write(body)

    def log_message(self, *_):
        pass


class TransitionTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True); self.thread.start()
        self.artifact = self.private("backup.dump", b"protected-backup")
        digest = hashlib.sha256(self.artifact.read_bytes()).hexdigest()
        self.proof = self.private_json("proof.json", {"schema": "norn.legacy-control-backup/v1",
            "sourceReleaseSHA": LEGACY, "backupSHA256": digest, "backupBytes": self.artifact.stat().st_size})
        self.shadow = self.private_json("shadow.json", {"schema": "norn.m5-signed-shadow-launchagent-rehearsal/v2",
            "candidateSHA": CANDIDATE, "runtimeClaim": "passed", "privateRestoreClaim": "passed",
            "backupSHA256": digest, "backupBytes": self.artifact.stat().st_size})
        self.called = self.root / "called"
        self.upgrade = self.root / "upgrade"
        self.upgrade.write_text(f'#!/bin/sh\nprintf %s "$NORN_API_BASE" > {self.called}\nexit ${{FAKE_UPGRADE_STATUS:-0}}\n', encoding="utf-8")
        self.upgrade.chmod(0o700)
        Handler.state["/api/apps"][0]["spec"]["deploy"] = True
        Handler.state["/api/health"]["status"] = "ok"

    def tearDown(self):
        self.server.shutdown(); self.server.server_close(); self.temporary.cleanup()

    def private(self, name, content):
        path = self.root / name; path.write_bytes(content); path.chmod(0o600); return path

    def private_json(self, name, value):
        return self.private(name, (json.dumps(value) + "\n").encode())

    def invoke(self, **extra):
        env = os.environ.copy(); env.update({"NORN_M5_PROTECTED_TRANSITION": "1", "NORN_M5_TRANSITION_TEST_HOOKS": "1"})
        env.update(extra.pop("env", {}))
        command = [str(SCRIPT), "--candidate-ref", CANDIDATE, "--candidate-sha", CANDIDATE,
                   "--legacy-release", LEGACY, "--backup-proof", str(self.proof),
                   "--backup-artifact", str(self.artifact), "--shadow-receipt", str(self.shadow),
                   "--ledger", str(self.root / "ledger.json"), "--receipt", str(self.root / "receipt.json"),
                   "--api", f"http://127.0.0.1:{self.server.server_port}", "--upgrade-script", str(self.upgrade)]
        return subprocess.run(command, env=env, text=True, capture_output=True)

    def test_success_records_bound_preservation_receipt(self):
        result = self.invoke()
        self.assertEqual(result.returncode, 0, result.stderr)
        receipt = json.loads((self.root / "receipt.json").read_text())
        self.assertEqual(receipt["state"], "complete")
        self.assertTrue(receipt["preservation"]["matched"])
        self.assertEqual(receipt["beforeDigest"], receipt["afterDigest"])
        self.assertEqual((self.root / "receipt.json").stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.called.read_text(), f"http://127.0.0.1:{self.server.server_port}")

    def test_mismatched_shadow_backup_refuses_before_upgrade(self):
        value = json.loads(self.shadow.read_text()); value["backupSHA256"] = "0" * 64
        self.shadow.write_text(json.dumps(value)); self.shadow.chmod(0o600)
        result = self.invoke()
        self.assertEqual(result.returncode, 2)
        self.assertFalse(self.called.exists())
        self.assertFalse((self.root / "ledger.json").exists())

    def test_existing_receipt_refuses_before_upgrade(self):
        self.private_json("receipt.json", {"state": "unrelated"})
        result = self.invoke()
        self.assertEqual(result.returncode, 2)
        self.assertFalse(self.called.exists())
        self.assertFalse((self.root / "ledger.json").exists())

    def test_degraded_workload_observation_refuses_before_upgrade(self):
        Handler.state["/api/health"]["status"] = "degraded"
        result = self.invoke()
        self.assertEqual(result.returncode, 2)
        self.assertIn("workload observation is degraded", result.stderr)
        self.assertFalse(self.called.exists())
        self.assertFalse((self.root / "ledger.json").exists())

    def test_upgrade_failure_retains_recovery_required_ledger(self):
        result = self.invoke(env={"FAKE_UPGRADE_STATUS": "23"})
        self.assertEqual(result.returncode, 23)
        ledger = json.loads((self.root / "ledger.json").read_text())
        self.assertEqual(ledger["state"], "candidate-recovery-required")
        self.assertEqual(ledger["upgradeExitCode"], 23)
        self.assertFalse((self.root / "receipt.json").exists())

    def test_changed_workload_identity_fails_postflight(self):
        self.upgrade.write_text(
            "#!/bin/sh\npython3 -c 'import urllib.request; urllib.request.urlopen(\"http://127.0.0.1:%s/change\")'\n" % self.server.server_port,
            encoding="utf-8")
        self.upgrade.chmod(0o700)
        original = Handler.do_GET
        def mutate(handler):
            if handler.path == "/change":
                Handler.state["/api/apps"][0]["spec"]["deploy"] = False
                handler.send_response(204); handler.end_headers(); return
            original(handler)
        Handler.do_GET = mutate
        try:
            result = self.invoke()
        finally:
            Handler.do_GET = original
        self.assertEqual(result.returncode, 2)
        ledger = json.loads((self.root / "ledger.json").read_text())
        self.assertEqual(ledger["state"], "postflight-preservation-failed")


if __name__ == "__main__":
    unittest.main()
