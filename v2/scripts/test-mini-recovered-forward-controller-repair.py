#!/usr/bin/env python3
"""Negative tests for the controller substitution authorization boundary."""

import importlib.machinery
import importlib.util
import json
import hmac
import hashlib
import plistlib
import subprocess
from argparse import Namespace
from subprocess import CompletedProcess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch


SCRIPT = Path(__file__).with_name("mini-recovered-forward-controller-repair")
LOADER = importlib.machinery.SourceFileLoader("mini_recovered_forward_controller_repair", str(SCRIPT))
SPEC = importlib.util.spec_from_loader(LOADER.name, LOADER)
assert SPEC and SPEC.loader
REPAIR = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(REPAIR)
LAUNCHER_SCRIPT = SCRIPT.with_name("mini-recovered-forward-api-launcher")
LAUNCHER_LOADER = importlib.machinery.SourceFileLoader("mini_recovered_forward_api_launcher", str(LAUNCHER_SCRIPT))
LAUNCHER_SPEC = importlib.util.spec_from_loader(LAUNCHER_LOADER.name, LAUNCHER_LOADER)
assert LAUNCHER_SPEC and LAUNCHER_SPEC.loader
LAUNCHER = importlib.util.module_from_spec(LAUNCHER_SPEC)
LAUNCHER_SPEC.loader.exec_module(LAUNCHER)


class ControllerRepairAuthorizationTest(unittest.TestCase):
    def test_signed_launcher_loads_owner_only_public_key_path(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            config = home / ".config/norn/release.env"
            config.parent.mkdir(parents=True)
            config.write_text("NORN_RELEASE_PUBLIC_KEY=/private/release-public.pem\n")
            config.chmod(0o600)
            self.assertEqual(LAUNCHER.release_public_key(home), "/private/release-public.pem")
            config.chmod(0o644)
            with self.assertRaisesRegex(SystemExit, "owner-only"):
                LAUNCHER.release_public_key(home)

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.directory = Path(self.temp.name).resolve() / "generation"
        self.directory.mkdir(mode=0o700)
        self.path = self.directory / "controller-repair.json"
        self.key = b"test-audit-key-longer-than-thirty-two-bytes"
        self.expected = {
            "schema": REPAIR.SCHEMA,
            "controllerSHA": "c" * 40,
            "candidateSHA": "b" * 40,
            "sourceSHA": "a" * 40,
            "transitionID": "mini-v3-20261013-3602e7e3",
            "pointerSHA256": "1" * 64,
            "manifestSHA256": "2" * 64,
            "journalSHA256": "3" * 64,
            "evidenceSHA256": "4" * 64,
            "generationID": "gen-example",
            "currentReleaseSHA": "a" * 40,
        }

    def tearDown(self):
        self.temp.cleanup()

    def test_signed_launcher_uses_imported_read_only_mode(self):
        home = self.directory
        original = home / "bin/norn-api-sops-launcher"
        original.parent.mkdir()
        original.write_bytes(b"original launcher")
        original.chmod(0o700)
        signed = home / "releases" / ("c" * 40) / "bin" / "mini-recovered-forward-api-launcher"
        signed.parent.mkdir(parents=True)
        signed.write_bytes(b"signed launcher")
        signed.chmod(0o555)
        controller = signed.parent / "platform-upgrade"
        with patch.object(Path, "home", return_value=home), patch.object(
                REPAIR, "ORIGINAL_LAUNCHER_SHA256", hashlib.sha256(original.read_bytes()).hexdigest()):
            self.assertEqual(REPAIR.verify_launchers(controller), (
                hashlib.sha256(original.read_bytes()).hexdigest(),
                hashlib.sha256(signed.read_bytes()).hexdigest()))
            signed.chmod(0o755)
            with self.assertRaisesRegex(ValueError, "unsafe"):
                REPAIR.verify_launchers(controller)

    def test_valid_authorization_is_private_and_cannot_be_reissued(self):
        REPAIR.create(self.path, self.expected, self.key)
        self.assertEqual(self.path.stat().st_mode & 0o777, 0o600)
        REPAIR.verify_authorization(self.path, self.expected, self.key)
        with self.assertRaisesRegex(ValueError, "already exists"):
            REPAIR.create(self.path, self.expected, self.key)

    def test_journal_advance_or_controller_change_invalidates_authorization(self):
        REPAIR.create(self.path, self.expected, self.key)
        for field, value in (("journalSHA256", "5" * 64), ("controllerSHA", "d" * 40),
                             ("candidateSHA", "e" * 40), ("pointerSHA256", "6" * 64),
                             ("currentReleaseSHA", "b" * 40)):
            with self.subTest(field=field), self.assertRaisesRegex(ValueError, "stale"):
                REPAIR.verify_authorization(self.path, dict(self.expected, **{field: value}), self.key)

    def test_wrong_key_and_tampered_document_are_rejected(self):
        REPAIR.create(self.path, self.expected, self.key)
        with self.assertRaisesRegex(ValueError, "HMAC"):
            REPAIR.verify_authorization(self.path, self.expected, b"another-audit-key-longer-than-thirty-two")
        changed = json.loads(self.path.read_bytes())
        changed["sourceSHA"] = "d" * 40
        self.path.write_bytes(REPAIR.canonical(changed))
        with self.assertRaisesRegex(ValueError, "HMAC"):
            REPAIR.verify_authorization(self.path, self.expected, self.key)

    def test_caller_cannot_replace_configured_signature_verifier(self):
        config = self.directory / ".config/norn"
        config.mkdir(parents=True)
        verifier = self.directory / "verify-release"
        verifier.write_text("#!/bin/sh\nexit 0\n")
        verifier.chmod(0o700)
        file = config / "release.env"
        file.write_text(f"NORN_RELEASE_VERIFY_HOOK={verifier}\n")
        file.chmod(0o600)
        self.assertEqual(REPAIR.configured_verifier(self.directory), verifier)
        with patch.dict("os.environ", {"NORN_RELEASE_VERIFY_HOOK": "/bin/true"}):
            with self.assertRaisesRegex(ValueError, "differs"):
                REPAIR.configured_verifier(self.directory)

    def test_activation_predecessor_requires_exact_journal_and_signed_release(self):
        releases = self.directory / "releases"
        source, candidate, predecessor = "a" * 40, "b" * 40, "d" * 40
        journal = {"retargetedFromCandidateSHA": predecessor}
        verifier = self.directory / "verifier"
        with patch.object(REPAIR, "release", return_value=releases / predecessor) as verified:
            allowed = REPAIR.allowed_current_releases(
                "candidate-activation-started", journal, releases, source, candidate, verifier)
        self.assertEqual(allowed, {releases / source, releases / candidate, releases / predecessor})
        verified.assert_called_once_with(releases, predecessor, verifier)
        with self.assertRaisesRegex(ValueError, "before candidate activation"):
            REPAIR.allowed_current_releases("schema-migrated", journal, releases, source, candidate, verifier)
        with self.assertRaisesRegex(ValueError, "predecessor is invalid"):
            REPAIR.allowed_current_releases("candidate-activation-started",
                                            {"retargetedFromCandidateSHA": source},
                                            releases, source, candidate, verifier)
        with patch.object(REPAIR, "release", side_effect=ValueError("signature failed")):
            with self.assertRaisesRegex(ValueError, "signature failed"):
                REPAIR.allowed_current_releases("candidate-activation-started", journal,
                                                releases, source, candidate, verifier)

    def test_failed_postflight_requires_candidate_current_even_with_predecessor(self):
        releases = self.directory / "releases"
        source, candidate, predecessor = "a" * 40, "b" * 40, "d" * 40
        with patch.object(REPAIR, "release", return_value=releases / predecessor):
            allowed = REPAIR.allowed_current_releases(
                "candidate-postflight-failed", {"retargetedFromCandidateSHA": predecessor},
                releases, source, candidate, self.directory / "verifier")
        self.assertEqual(allowed, {releases / candidate})

    def test_plist_stage_normal_restore_survive_each_atomic_crash_seam(self):
        plist = self.directory / "com.norn.api.plist"
        original = plistlib.dumps({
            "Label": "com.norn.api",
            "ProgramArguments": [str(Path.home() / "bin/norn-api-sops-launcher")],
            "EnvironmentVariables": {"NORN_STARTUP_MODE": "active", "NORN_SCHEMA_MODE": "check"},
        })
        plist.write_bytes(original)
        plist.chmod(0o644)
        journal = self.directory / "transition.json"
        manifest = {"sourceEvidencePath": str(self.directory / "transition.json.source-evidence.json"),
                    "sourceEvidenceSHA256": self.expected["evidenceSHA256"]}
        with patch.object(REPAIR, "api_plist_path", return_value=plist), patch.object(
                REPAIR, "verify_launchers", return_value=("a" * 64, "b" * 64)):
            REPAIR.stage_plist(journal, self.expected, manifest, self.key)
            maintenance = plist.read_bytes()
            self.assertNotEqual(maintenance, original)
            self.assertEqual(plistlib.loads(maintenance)["EnvironmentVariables"]["NORN_RECOVERED_FORWARD_MAINTENANCE"], "true")
            self.assertEqual(plistlib.loads(maintenance)["ProgramArguments"],
                             [str(SCRIPT.with_name("mini-recovered-forward-api-launcher"))])
            journal.write_bytes(b'{"state":"candidate-postflight-failed"}')
            journal.chmod(0o600)
            inherited = plistlib.loads(maintenance)["EnvironmentVariables"]
            with patch.dict("os.environ", inherited):
                REPAIR.verify_launcher_runtime(journal, self.expected, manifest, self.key)
            with patch.dict("os.environ", {"NORN_RECOVERED_FORWARD_MAINTENANCE": "true"}):
                with self.assertRaisesRegex(ValueError, "inherited recovery bindings"):
                    REPAIR.verify_launcher_runtime(journal, self.expected, manifest, self.key)
            # Crash after the signed sidecar but before the plist replace.
            plist.write_bytes(original)
            REPAIR.stage_plist(journal, self.expected, manifest, self.key)
            self.assertEqual(plist.read_bytes(), maintenance)
            # Crash after plist replace and before launchd bootstrap.
            REPAIR.stage_plist(journal, self.expected, manifest, self.key)
            self.assertEqual(plist.read_bytes(), maintenance)
            with self.assertRaisesRegex((OSError, ValueError), "preservation receipt"):
                REPAIR.advance_plist(journal, self.expected, manifest, self.key, "plist-normal")
            self.assertEqual(plist.read_bytes(), maintenance)
            payload = {"schema": "norn.recovered-forward-preservation/v1",
                       "transitionId": self.expected["transitionID"],
                       "candidateSHA": self.expected["candidateSHA"],
                       "sourceEvidenceSHA256": self.expected["evidenceSHA256"],
                       "beforeDigest": "f" * 64, "afterDigest": "f" * 64,
                       "preservationMatched": True, "finalizedAt": "2026-10-13T21:00:00Z"}
            signature = "hmac-sha256:" + hmac.new(self.key, payload["schema"].encode() + b"\0"
                                                  + REPAIR.canonical(payload), hashlib.sha256).hexdigest()
            receipt = journal.parent / "preservation-receipt.json"
            receipt.write_bytes(REPAIR.canonical(dict(payload, signature=signature)))
            receipt.chmod(0o600)
            REPAIR.advance_plist(journal, self.expected, manifest, self.key, "plist-normal")
            normal = plist.read_bytes()
            self.assertNotEqual(normal, maintenance)
            normal_environment = plistlib.loads(normal)["EnvironmentVariables"]
            self.assertNotIn("NORN_RECOVERED_FORWARD_MAINTENANCE", normal_environment)
            self.assertEqual(normal_environment["NORN_RECOVERED_FORWARD_ACTIVATE_TRANSITION_ID"], self.expected["transitionID"])
            journal.write_bytes(b'{"state":"candidate-normal-activation-pending"}')
            with patch.dict("os.environ", normal_environment):
                REPAIR.verify_launcher_runtime(journal, self.expected, manifest, self.key)
            # Crash after normal plist replacement and before bootstrap.
            REPAIR.advance_plist(journal, self.expected, manifest, self.key, "plist-normal")
            self.assertEqual(plist.read_bytes(), normal)
            REPAIR.advance_plist(journal, self.expected, manifest, self.key, "plist-restore")
            self.assertEqual(plist.read_bytes(), original)
            # Crash after original plist restoration and before journal promotion.
            REPAIR.advance_plist(journal, self.expected, manifest, self.key, "plist-restore")
            self.assertEqual(plist.read_bytes(), original)

    def test_plist_tamper_or_changed_transition_fails_closed(self):
        plist = self.directory / "com.norn.api.plist"
        plist.write_bytes(plistlib.dumps({"Label": "com.norn.api",
                                          "ProgramArguments": [str(Path.home() / "bin/norn-api-sops-launcher")]}))
        plist.chmod(0o644)
        journal = self.directory / "transition.json"
        manifest = {"sourceEvidencePath": str(self.directory / "transition.json.source-evidence.json"),
                    "sourceEvidenceSHA256": self.expected["evidenceSHA256"]}
        with patch.object(REPAIR, "api_plist_path", return_value=plist), patch.object(
                REPAIR, "verify_launchers", return_value=("a" * 64, "b" * 64)):
            REPAIR.stage_plist(journal, self.expected, manifest, self.key)
            with self.assertRaisesRegex(ValueError, "another transition"):
                REPAIR.stage_plist(journal, dict(self.expected, transitionID="other-transition"), manifest, self.key)
            plist.write_bytes(b"tampered")
            with self.assertRaisesRegex(ValueError, "neither the signed original nor"):
                REPAIR.stage_plist(journal, self.expected, manifest, self.key)

    def test_d_only_runner_sequences_repair_preservation_and_finalization(self):
        journal = self.directory / "transition.json"
        journal.write_text('{"state":"candidate-postflight-failed"}')
        journal.chmod(0o600)
        args = Namespace(candidate=self.expected["candidateSHA"], source=self.expected["sourceSHA"],
                         transition=self.expected["transitionID"], journal=journal,
                         controller=self.directory / "platform-upgrade", authorization=None)
        calls = []

        def run(command, **kwargs):
            calls.append(command)
            if command[1] == "prepare":
                journal.write_text('{"state":"candidate-preservation-pending"}')
            elif command[1] == "recovered-forward-finalize":
                journal.write_text('{"state":"candidate-promoted"}')
            return CompletedProcess(command, 0)

        with patch.object(REPAIR.subprocess, "run", side_effect=run):
            REPAIR.run_recovery(args)
        self.assertEqual([command[1] for command in calls],
                         ["prepare", "preserve", "recovered-forward-finalize"])
        self.assertEqual(calls[2][0], str(args.controller))
        # A retry after finalization invokes only D's idempotent cleanup.
        calls.clear()
        with patch.object(REPAIR.subprocess, "run", side_effect=run):
            REPAIR.run_recovery(args)
        self.assertEqual([command[1] for command in calls], ["recovered-forward-finalize"])

    def test_d_only_runner_stops_at_failed_substep_without_wrong_finalizer(self):
        journal = self.directory / "transition.json"
        journal.write_text('{"state":"candidate-preservation-pending"}')
        journal.chmod(0o600)
        args = Namespace(candidate=self.expected["candidateSHA"], source=self.expected["sourceSHA"],
                         transition=self.expected["transitionID"], journal=journal,
                         controller=self.directory / "platform-upgrade", authorization=None)
        with patch.object(REPAIR.subprocess, "run", return_value=CompletedProcess([], 2)) as called:
            with self.assertRaisesRegex(ValueError, "preservation stopped"):
                REPAIR.run_recovery(args)
        called.assert_called_once()
        self.assertEqual(called.call_args.args[0][1], "preserve")

    def test_loaded_idle_original_job_is_booted_out_before_signed_stage(self):
        platform = SCRIPT.with_name("platform-upgrade").read_text()
        restart = platform.split("restart_api() {\n", 1)[1].split("\n}\n", 1)[0]
        plist = self.directory / "com.norn.api.plist"
        original_launcher = str(Path.home() / "bin/norn-api-sops-launcher")
        plist.write_bytes(plistlib.dumps({"Label": "com.norn.api", "ProgramArguments": [original_launcher]}))
        script = f'''set -e
mode=recovered-forward
internal_recovered_forward_repair=true
api_launch_plist={str(plist)!r}
api_launch_program={original_launcher!r}
script_dir={str(SCRIPT.parent)!r}
launch_label=com.norn.api
legacy_api_path=/tmp/norn-api
loaded=1
launchctl() {{
  case "$1" in
    print) if [[ "$loaded" == 1 ]]; then printf 'program = %s\\n' "$api_launch_program"; else return 113; fi ;;
    bootout) echo bootout; loaded=0 ;;
    kickstart) echo kickstart ;;
  esac
}}
legacy_baseline_validate_launchd_binding() {{ :; }}
recovered_forward_api_plist_action() {{ [[ "$loaded" == 0 ]] || return 1; echo "$1"; }}
legacy_baseline_bootstrap_launchd_service() {{ [[ "$loaded" == 0 ]] || return 1; [[ "$4" == "$script_dir/mini-recovered-forward-api-launcher" ]] || return 1; echo bootstrap-signed; }}
fail() {{ echo "$*" >&2; exit 1; }}
restart_api() {{
{restart}
}}
restart_api
'''
        result = subprocess.run(["bash", "-c", script], text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.splitlines(), ["bootout", "plist-stage", "bootstrap-signed", "kickstart"])

    def test_failed_postflight_rebinds_exact_current_candidate_under_strict_shell(self):
        platform = SCRIPT.with_name("platform-upgrade").read_text()
        check = platform.split("recovered_forward_require_current_candidate() {\n", 1)[1].split("\n}\n", 1)[0]
        releases = self.directory / "releases"
        candidate = releases / ("b" * 40)
        other = releases / ("c" * 40)
        candidate.mkdir(parents=True)
        other.mkdir()
        (candidate / "release.env").write_text("NORN_RELEASE_SHA=" + candidate.name + "\n")
        (other / "release.env").write_text("NORN_RELEASE_SHA=" + other.name + "\n")
        current = self.directory / "current"
        current.symlink_to(candidate)
        script = f'''set -euo pipefail
current_link={str(current)!r}
release={str(candidate)!r}
sha={candidate.name!r}
fail() {{ printf '%s\\n' "$*" >&2; exit 1; }}
release_sha() {{ sed -n 's/^NORN_RELEASE_SHA=//p' "$1/release.env"; }}
recovered_forward_require_current_candidate() {{
{check}
}}
recovered_forward_require_current_candidate
'''
        result = subprocess.run(["bash", "-c", script], text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        current.unlink()
        current.symlink_to(other)
        (other / "release.env").write_text("NORN_RELEASE_SHA=" + candidate.name + "\n")
        result = subprocess.run(["bash", "-c", script], text=True, capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("not the signed candidate", result.stderr)
        current.unlink()
        current.symlink_to(candidate)
        (candidate / "release.env").write_text("NORN_RELEASE_SHA=" + other.name + "\n")
        result = subprocess.run(["bash", "-c", script], text=True, capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("not the signed candidate", result.stderr)

    def test_host_restart_phase_does_not_regress_before_candidate_install(self):
        platform = SCRIPT.with_name("platform-upgrade").read_text()
        finalizer = platform.split("recovered_forward_finalize_release() {\n", 1)[1].split("\n}\n", 1)[0]
        tail = finalizer.split('validate_active_responses "$version" "$final_version_file" "$final_schema_file" "$sha"', 1)[1]
        self.assertIn('if [[ "$recovered_phase" != "candidate-normal-active-pending-host-agent-restart" ]]; then\n'
                      '    legacy_baseline_write_fence_state "candidate-normal-active-pending-host-agent"', tail)
        self.assertLess(tail.index('legacy_baseline_write_fence_state "candidate-normal-active-pending-host-agent-restart"'),
                        tail.index("restart_fenced_host_agent"))

    def test_candidate_host_binary_is_allowed_only_after_restart_intent(self):
        journal = self.directory / "transition.json"
        args = Namespace(action="run", journal=journal, authorization=None)
        observed = []
        def checked(*_args, **kwargs):
            observed.append(kwargs["allow_candidate_host"])
            raise RuntimeError("state checked")
        for phase, allowed in (("candidate-postflight-failed", False),
                               ("candidate-preservation-pending", False),
                               ("candidate-normal-active-pending-host-agent", False),
                               ("candidate-normal-active-pending-host-agent-restart", True),
                               ("candidate-promoted", True)):
            journal.write_text(json.dumps({"state": phase}))
            journal.chmod(0o600)
            with self.subTest(phase=phase), patch.object(REPAIR, "parser") as parser, \
                    patch.object(REPAIR, "checked_state", side_effect=checked), \
                    patch.dict("os.environ", {"NORN_AUDIT_SIGNING_KEY": self.key.decode()}):
                parser.return_value.parse_args.return_value = args
                with self.assertRaisesRegex(RuntimeError, "state checked"):
                    REPAIR.main()
                self.assertEqual(observed.pop(), allowed)



if __name__ == "__main__":
    unittest.main()
