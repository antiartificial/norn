#!/usr/bin/env python3

import hashlib
import json
import os
import shutil
import subprocess
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlparse


SCRIPT = Path(__file__).resolve().parent / "host-runtime"


class CapacityRuntimeHandler(BaseHTTPRequestHandler):
    def log_message(self, _format: str, *_args: object) -> None:
        pass

    def _send_json(self, status: int, body: object) -> None:
        encoded = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def do_GET(self) -> None:
        role = self.server.role  # type: ignore[attr-defined]
        if role == "consul" and self.path == "/v1/status/leader":
            self._send_json(200, "127.0.0.1:8300")
            return
        if role == "nomad" and self.path == "/v1/status/leader":
            self._send_json(200, "127.0.0.1:4647")
            return
        if role == "nomad" and self.path == "/v1/nodes":
            self._send_json(200, [{"Status": "ready", "Attributes": {"unique.network.ip-address": "127.0.0.1"}}])
            return
        if role == "norn" and self.path == "/api/health":
            self._send_json(200, {"ok": True})
            return
        if role == "norn" and self.path == "/api/apps":
            self._send_json(200, self.server.apps)  # type: ignore[attr-defined]
            return
        if role == "norn" and self.path.startswith("/api/events/correlated?"):
            if self.server.fail_correlated_get:  # type: ignore[attr-defined]
                self._send_json(500, {"error": "intentional correlated lookup failure"})
                return
            key = parse_qs(urlparse(self.path).query).get("key", [""])[0]
            events = [
                event for event in self.server.events  # type: ignore[attr-defined]
                if (event.get("metadata") or {}).get("correlationKey") == key
            ]
            self._send_json(200, {"events": events, "correlationKey": key})
            return
        self._send_json(404, {"error": "not found"})

    def do_POST(self) -> None:
        if self.server.role != "norn" or self.path != "/api/events":  # type: ignore[attr-defined]
            self._send_json(404, {"error": "not found"})
            return
        length = int(self.headers.get("Content-Length", "0"))
        event = json.loads(self.rfile.read(length))
        if self.server.fail_events:  # type: ignore[attr-defined]
            self._send_json(500, {"error": "intentional failure"})
            return
        if event.get("metadata", {}).get("legacyCapacityWarningID") and self.server.fail_legacy_adoption:  # type: ignore[attr-defined]
            self._send_json(500, {"error": "intentional legacy adoption failure"})
            return
        dedupe_key = event.get("dedupeKey", "")
        if dedupe_key in self.server.dedupe_keys:  # type: ignore[attr-defined]
            # The API returns a 2xx null for an accepted duplicate; it still
            # proves the event was handled, so local state may advance.
            self._send_json(200, None)
            return
        self.server.dedupe_keys.add(dedupe_key)  # type: ignore[attr-defined]
        self.server.events.append(event)  # type: ignore[attr-defined]
        self._send_json(201, event)


class HostRuntimeCapacityTests(unittest.TestCase):
    host_scope = "fixture-host"
    @classmethod
    def setUpClass(cls) -> None:
        if not shutil.which("bash") or not shutil.which("curl"):
            raise unittest.SkipTest("bash and curl are required")

    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.state = self.root / "state"
        self.config = self.root / "config"
        self.norn = self.start_server("norn")
        self.consul = self.start_server("consul")
        self.nomad = self.start_server("nomad")
        self.norn.apps = [self.under_capacity_app("first-app")]
        self.norn.events = []
        self.norn.dedupe_keys = set()
        self.norn.fail_events = True

    def tearDown(self) -> None:
        for server in (self.norn, self.consul, self.nomad):
            server.shutdown()
            server.server_close()
        self.temporary.cleanup()

    def start_server(self, role: str) -> ThreadingHTTPServer:
        server = ThreadingHTTPServer(("127.0.0.1", 0), CapacityRuntimeHandler)
        server.role = role  # type: ignore[attr-defined]
        server.apps = []  # type: ignore[attr-defined]
        server.events = []  # type: ignore[attr-defined]
        server.dedupe_keys = set()  # type: ignore[attr-defined]
        server.fail_events = False  # type: ignore[attr-defined]
        server.fail_legacy_adoption = False  # type: ignore[attr-defined]
        server.fail_correlated_get = False  # type: ignore[attr-defined]
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        return server

    @staticmethod
    def under_capacity_app(name: str) -> dict[str, object]:
        return {
            "spec": {
                "name": name,
                "deploy": True,
                "processes": {"web": {"scaling": {"min": 1}}},
            },
            "allocations": [],
        }

    def url(self, server: ThreadingHTTPServer) -> str:
        return f"http://127.0.0.1:{server.server_port}"

    def assure(self, host_id: str | None = host_scope, beacon_environment: str | None = "development") -> subprocess.CompletedProcess[str]:
        environment = os.environ | {
            "NORN_URL": self.url(self.norn),
            "CONSUL_HTTP_ADDR": self.url(self.consul),
            "NOMAD_ADDR": self.url(self.nomad),
        }
        if host_id is not None:
            environment["NORN_HOST_ID"] = host_id
        if beacon_environment is not None:
            environment["NORN_BEACON_ENVIRONMENT"] = beacon_environment
        return subprocess.run(
            [
                str(SCRIPT),
                "assure",
                "--state-dir",
                str(self.state),
                "--config-dir",
                str(self.config),
                "--skip-cloudflared",
            ],
            env=environment,
            text=True,
            capture_output=True,
            check=False,
        )

    def capacity_events(self, event_type: str) -> list[dict[str, object]]:
        return [event for event in self.norn.events if event["type"] == event_type]

    @staticmethod
    def capacity_hash(name: str) -> str:
        snapshot = f"{name}\tweb\t1\t0\n".encode()
        return hashlib.sha256(snapshot).hexdigest()[:16]

    def test_capacity_episode_retries_before_state_advance_and_rotates_after_recovery(self) -> None:
        failed = self.assure()
        self.assertNotEqual(failed.returncode, 0, failed.stdout + failed.stderr)
        episode_path = self.state / "assurance-capacity-episode"
        capacity_state = self.state / "assurance-below-minimum"
        self.assertTrue(episode_path.is_file())
        self.assertFalse(capacity_state.exists(), "capacity state advanced after failed event emission")
        first_episode = episode_path.read_text().strip()

        # A changed missing-process set stays in the same unresolved episode,
        # but its snapshot hash gives it a distinct warning dedupe key.
        self.norn.apps = [self.under_capacity_app("second-app")]
        self.norn.fail_events = False
        emitted = self.assure()
        self.assertEqual(emitted.returncode, 0, emitted.stdout + emitted.stderr)
        self.assertEqual(episode_path.read_text().strip(), first_episode)
        self.assertTrue(capacity_state.is_file())
        warning = self.capacity_events("service.capacity.below_minimum")[-1]
        self.assertEqual(warning["source"], f"norn-host:{self.host_scope}")
        self.assertEqual(warning["metadata"]["hostScope"], self.host_scope)
        self.assertEqual(warning["metadata"]["correlationKey"], f"norn-host:{self.host_scope}:minimum-capacity")
        self.assertIn("second-app:web", warning["body"])
        self.assertEqual(
            warning["dedupeKey"],
            f"norn-host:{self.host_scope}:service.capacity.below_minimum:{first_episode}:{self.capacity_hash('second-app')}",
        )

        # A duplicated warning response is a successful accepted result, so a
        # retry after an interrupted local state write may safely restore state.
        capacity_state.unlink()
        duplicate = self.assure()
        self.assertEqual(duplicate.returncode, 0, duplicate.stdout + duplicate.stderr)
        self.assertTrue(capacity_state.is_file())
        self.assertEqual(len(self.capacity_events("service.capacity.below_minimum")), 1)

        self.norn.apps = [self.under_capacity_app("changed-app")]
        changed = self.assure()
        self.assertEqual(changed.returncode, 0, changed.stdout + changed.stderr)
        changed_warning = self.capacity_events("service.capacity.below_minimum")[-1]
        self.assertEqual(len(self.capacity_events("service.capacity.below_minimum")), 2)
        self.assertIn("changed-app:web", changed_warning["body"])
        self.assertEqual(
            changed_warning["dedupeKey"],
            f"norn-host:{self.host_scope}:service.capacity.below_minimum:{first_episode}:{self.capacity_hash('changed-app')}",
        )
        self.assertIn("changed-app\tweb\t1\t0", capacity_state.read_text())

        self.norn.apps = []
        recovered = self.assure()
        self.assertEqual(recovered.returncode, 0, recovered.stdout + recovered.stderr)
        recovery = self.capacity_events("service.capacity.recovered")[-1]
        self.assertEqual(recovery["source"], f"norn-host:{self.host_scope}")
        self.assertEqual(recovery["metadata"]["hostScope"], self.host_scope)
        self.assertEqual(recovery["metadata"]["correlationKey"], f"norn-host:{self.host_scope}:minimum-capacity")
        self.assertEqual(recovery["dedupeKey"], f"norn-host:{self.host_scope}:service.capacity.recovered:{first_episode}")
        self.assertFalse(capacity_state.exists())
        self.assertFalse(episode_path.exists())

        self.norn.apps = [self.under_capacity_app("third-app")]
        next_episode = self.assure()
        self.assertEqual(next_episode.returncode, 0, next_episode.stdout + next_episode.stderr)
        second_episode = episode_path.read_text().strip()
        self.assertNotEqual(second_episode, first_episode)
        new_warning = self.capacity_events("service.capacity.below_minimum")[-1]
        self.assertEqual(
            new_warning["dedupeKey"],
            f"norn-host:{self.host_scope}:service.capacity.below_minimum:{second_episode}:{self.capacity_hash('third-app')}",
        )

    def test_capacity_events_persist_a_default_host_identity(self) -> None:
        self.norn.fail_events = False
        warning_result = self.assure(host_id=None)
        self.assertEqual(warning_result.returncode, 0, warning_result.stdout + warning_result.stderr)
        identity_path = self.state / "assurance-host-identity"
        self.assertTrue(identity_path.is_file())
        scope = identity_path.read_text().strip()
        self.assertTrue(scope)
        warning = self.capacity_events("service.capacity.below_minimum")[-1]
        self.assertEqual(warning["source"], f"norn-host:{scope}")

        self.norn.apps = []
        recovery_result = self.assure(host_id=None)
        self.assertEqual(recovery_result.returncode, 0, recovery_result.stdout + recovery_result.stderr)
        recovery = self.capacity_events("service.capacity.recovered")[-1]
        self.assertEqual(recovery["source"], f"norn-host:{scope}")
        self.assertEqual(recovery["metadata"]["correlationKey"], f"norn-host:{scope}:minimum-capacity")

    def test_first_upgrade_adopts_only_one_matching_legacy_capacity_warning(self) -> None:
        legacy_snapshot = "legacy-app\tweb\t1\t0\n"
        self.state.mkdir(parents=True)
        (self.state / "assurance-below-minimum").write_text(legacy_snapshot)
        legacy_body = "Declared minimum capacity is not satisfied:\n- legacy-app:web has 0 available; minimum is 1"
        self.norn.apps = []
        self.norn.fail_events = False
        self.norn.events.append({
            "id": "legacy-capacity-warning",
            "source": "norn",
            "app": "norn-host",
            "environment": "development",
            "type": "service.capacity.below_minimum",
            "severity": "warning",
            "body": legacy_body,
            "metadata": {"correlationKey": "norn-host:minimum-capacity"},
        })

        result = self.assure()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        adoptions = [
            event for event in self.capacity_events("service.capacity.recovered")
            if event.get("metadata", {}).get("legacyCapacityWarningID")
        ]
        self.assertEqual(len(adoptions), 1)
        self.assertEqual(adoptions[0]["source"], "norn")
        self.assertEqual(adoptions[0]["metadata"]["legacyCapacityWarningID"], "legacy-capacity-warning")
        self.assertEqual((self.state / "assurance-capacity-legacy-adoption").read_text().splitlines()[0], "adopted")

    def test_ambiguous_legacy_capacity_warnings_are_left_for_review(self) -> None:
        legacy_snapshot = "legacy-app\tweb\t1\t0\n"
        self.state.mkdir(parents=True)
        (self.state / "assurance-below-minimum").write_text(legacy_snapshot)
        legacy_body = "Declared minimum capacity is not satisfied:\n- legacy-app:web has 0 available; minimum is 1"
        self.norn.apps = []
        self.norn.fail_events = False
        for warning_id in ("legacy-warning-a", "legacy-warning-b"):
            self.norn.events.append({
                "id": warning_id,
                "source": "norn",
                "app": "norn-host",
                "environment": "development",
                "type": "service.capacity.below_minimum",
                "severity": "warning",
                "body": legacy_body,
                "metadata": {"correlationKey": "norn-host:minimum-capacity"},
            })

        result = self.assure()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse(any(
            event.get("metadata", {}).get("legacyCapacityWarningID")
            for event in self.capacity_events("service.capacity.recovered")
        ))
        self.assertEqual((self.state / "assurance-capacity-legacy-adoption").read_text().strip(), "review-required")

    def test_legacy_adoption_retries_the_persisted_target_after_a_post_failure(self) -> None:
        legacy_snapshot = "legacy-app\tweb\t1\t0\n"
        self.state.mkdir(parents=True)
        (self.state / "assurance-below-minimum").write_text(legacy_snapshot)
        self.norn.apps = []
        self.norn.fail_events = False
        self.norn.fail_legacy_adoption = True
        self.norn.events.append({
            "id": "legacy-capacity-warning",
            "source": "norn",
            "app": "norn-host",
            "environment": "development",
            "type": "service.capacity.below_minimum",
            "severity": "warning",
            "body": "Declared minimum capacity is not satisfied:\n- legacy-app:web has 0 available; minimum is 1",
            "metadata": {"correlationKey": "norn-host:minimum-capacity"},
        })

        failed = self.assure()
        self.assertNotEqual(failed.returncode, 0, failed.stdout + failed.stderr)
        marker = (self.state / "assurance-capacity-legacy-adoption").read_text().splitlines()
        self.assertEqual(marker[0], "attempted")
        self.assertEqual(marker[1], "legacy-capacity-warning")
        episode = marker[2]
        self.assertTrue(episode)
        self.assertFalse((self.state / "assurance-below-minimum").exists())
        self.assertFalse((self.state / "assurance-capacity-episode").exists())

        self.norn.fail_legacy_adoption = False
        retried = self.assure()
        self.assertEqual(retried.returncode, 0, retried.stdout + retried.stderr)
        adoptions = [
            event for event in self.capacity_events("service.capacity.recovered")
            if event.get("metadata", {}).get("legacyCapacityWarningID")
        ]
        self.assertEqual(len(adoptions), 1)
        self.assertEqual(adoptions[0]["metadata"]["legacyCapacityWarningID"], "legacy-capacity-warning")
        self.assertEqual(adoptions[0]["dedupeKey"], f"norn-host:{self.host_scope}:legacy-capacity-adoption:{episode}")
        self.assertEqual((self.state / "assurance-capacity-legacy-adoption").read_text().splitlines()[0], "adopted")

    def test_explicit_host_id_cannot_override_persisted_identity(self) -> None:
        self.norn.fail_events = False
        first = self.assure()
        self.assertEqual(first.returncode, 0, first.stdout + first.stderr)
        self.assertEqual((self.state / "assurance-host-identity").read_text().strip(), self.host_scope)
        changed = self.assure(host_id="different-host")
        self.assertNotEqual(changed.returncode, 0)
        self.assertIn("could not establish a stable host identity", changed.stdout)

    def test_existing_identity_recovers_interrupted_legacy_marker_initialization(self) -> None:
        self.state.mkdir(parents=True)
        (self.state / "assurance-host-identity").write_text(f"{self.host_scope}\n")
        (self.state / "assurance-below-minimum").write_text("legacy-app\tweb\t1\t0\n")
        self.norn.apps = []
        self.norn.fail_events = False
        self.norn.events.append({
            "id": "legacy-crash-warning", "source": "norn", "app": "norn-host", "environment": "development",
            "type": "service.capacity.below_minimum", "severity": "warning",
            "body": "Declared minimum capacity is not satisfied:\n- legacy-app:web has 0 available; minimum is 1",
            "metadata": {"correlationKey": "norn-host:minimum-capacity"},
        })
        result = self.assure()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue(any(
            event.get("metadata", {}).get("legacyCapacityWarningID") == "legacy-crash-warning"
            for event in self.capacity_events("service.capacity.recovered")
        ))

    def test_scoped_capacity_state_without_marker_is_not_adopted_as_legacy(self) -> None:
        self.state.mkdir(parents=True)
        (self.state / "assurance-host-identity").write_text(f"{self.host_scope}\n")
        (self.state / "assurance-below-minimum").write_text("legacy-app\tweb\t1\t0\n")
        (self.state / "assurance-below-minimum-scope").write_text(f"{self.host_scope}\n")
        self.norn.apps = []
        self.norn.fail_events = False
        self.norn.events.append({
            "id": "would-be-legacy-warning", "source": "norn", "app": "norn-host", "environment": "development",
            "type": "service.capacity.below_minimum", "severity": "warning",
            "body": "Declared minimum capacity is not satisfied:\n- legacy-app:web has 0 available; minimum is 1",
            "metadata": {"correlationKey": "norn-host:minimum-capacity"},
        })
        result = self.assure()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse(any(
            event.get("metadata", {}).get("legacyCapacityWarningID")
            for event in self.capacity_events("service.capacity.recovered")
        ))

    def test_legacy_adoption_requires_the_current_beacon_environment(self) -> None:
        self.state.mkdir(parents=True)
        (self.state / "assurance-below-minimum").write_text("legacy-app\tweb\t1\t0\n")
        self.norn.apps = []
        self.norn.fail_events = False
        self.norn.events.append({
            "id": "production-warning", "source": "norn", "app": "norn-host", "environment": "production",
            "type": "service.capacity.below_minimum", "severity": "warning",
            "body": "Declared minimum capacity is not satisfied:\n- legacy-app:web has 0 available; minimum is 1",
            "metadata": {"correlationKey": "norn-host:minimum-capacity"},
        })
        result = self.assure()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse(any(event.get("metadata", {}).get("legacyCapacityWarningID") for event in self.norn.events))
        self.assertEqual((self.state / "assurance-capacity-legacy-adoption").read_text().splitlines()[0], "review-required")

    def test_legacy_adoption_ignores_duplicate_candidates_in_other_environments(self) -> None:
        self.state.mkdir(parents=True)
        (self.state / "assurance-below-minimum").write_text("legacy-app\tweb\t1\t0\n")
        self.norn.apps = []
        self.norn.fail_events = False
        for warning_id, environment in (("development-warning", "development"), ("production-warning", "production")):
            self.norn.events.append({
                "id": warning_id, "source": "norn", "app": "norn-host", "environment": environment,
                "type": "service.capacity.below_minimum", "severity": "warning",
                "body": "Declared minimum capacity is not satisfied:\n- legacy-app:web has 0 available; minimum is 1",
                "metadata": {"correlationKey": "norn-host:minimum-capacity"},
            })
        result = self.assure()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        adoptions = [event for event in self.norn.events if event.get("metadata", {}).get("legacyCapacityWarningID")]
        self.assertEqual(len(adoptions), 1)
        self.assertEqual(adoptions[0]["metadata"]["legacyCapacityWarningID"], "development-warning")
        self.assertEqual(adoptions[0]["environment"], "development")

    def test_legacy_adoption_without_environment_is_retryable_until_configured(self) -> None:
        self.state.mkdir(parents=True)
        (self.state / "assurance-below-minimum").write_text("legacy-app\tweb\t1\t0\n")
        self.norn.apps = []
        self.norn.fail_events = False
        self.norn.events.append({
            "id": "development-warning", "source": "norn", "app": "norn-host", "environment": "development",
            "type": "service.capacity.below_minimum", "severity": "warning",
            "body": "Declared minimum capacity is not satisfied:\n- legacy-app:web has 0 available; minimum is 1",
            "metadata": {"correlationKey": "norn-host:minimum-capacity"},
        })
        missing = self.assure(beacon_environment=None)
        self.assertEqual(missing.returncode, 0, missing.stdout + missing.stderr)
        self.assertEqual((self.state / "assurance-capacity-legacy-adoption").read_text().splitlines()[0], "config-required")
        self.assertTrue((self.state / "assurance-below-minimum").exists())

        configured = self.assure(beacon_environment="development")
        self.assertEqual(configured.returncode, 0, configured.stdout + configured.stderr)
        self.assertEqual((self.state / "assurance-beacon-environment").read_text().strip(), "development")
        self.assertTrue(any(
            event.get("metadata", {}).get("legacyCapacityWarningID") == "development-warning"
            for event in self.norn.events
        ))

    def test_legacy_adoption_retries_after_a_correlated_lookup_failure(self) -> None:
        self.state.mkdir(parents=True)
        (self.state / "assurance-below-minimum").write_text("legacy-app\tweb\t1\t0\n")
        self.norn.apps = []
        self.norn.fail_events = False
        self.norn.fail_correlated_get = True
        self.norn.events.append({
            "id": "development-warning", "source": "norn", "app": "norn-host", "environment": "development",
            "type": "service.capacity.below_minimum", "severity": "warning",
            "body": "Declared minimum capacity is not satisfied:\n- legacy-app:web has 0 available; minimum is 1",
            "metadata": {"correlationKey": "norn-host:minimum-capacity"},
        })
        failed = self.assure()
        self.assertNotEqual(failed.returncode, 0, failed.stdout + failed.stderr)
        self.assertEqual((self.state / "assurance-capacity-legacy-adoption").read_text().splitlines()[0], "pending")
        self.assertTrue((self.state / "assurance-below-minimum").exists())

        self.norn.fail_correlated_get = False
        retried = self.assure()
        self.assertEqual(retried.returncode, 0, retried.stdout + retried.stderr)
        self.assertTrue(any(
            event.get("metadata", {}).get("legacyCapacityWarningID") == "development-warning"
            for event in self.norn.events
        ))

    def test_periodic_assurance_reuses_the_persisted_beacon_environment(self) -> None:
        self.norn.fail_events = False
        self.norn.apps = [self.under_capacity_app("first-app")]
        warning = self.assure(beacon_environment="development")
        self.assertEqual(warning.returncode, 0, warning.stdout + warning.stderr)
        self.norn.apps = []
        recovery = self.assure(beacon_environment=None)
        self.assertEqual(recovery.returncode, 0, recovery.stdout + recovery.stderr)
        capacity = self.capacity_events("service.capacity.below_minimum")[-1]
        recovered = self.capacity_events("service.capacity.recovered")[-1]
        self.assertEqual(capacity["environment"], "development")
        self.assertEqual(recovered["environment"], "development")


if __name__ == "__main__":
    unittest.main()
