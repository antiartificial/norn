#!/usr/bin/env python3
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).with_name('mini-signed-shadow-launchagent-rehearsal')

class SignedShadowGuardTests(unittest.TestCase):
    def invoke(self, *args, env=None):
        values = os.environ.copy(); values.update(env or {})
        return subprocess.run([str(SCRIPT), *args], text=True, capture_output=True, env=values)

    def test_requires_opt_in_before_reading_targets(self):
        result = self.invoke('run', '--candidate-release', '/missing', '--public-key', '/missing')
        self.assertEqual(result.returncode, 2)
        self.assertIn('NORN_M5_SIGNED_SHADOW_REHEARSAL', result.stderr)

    def test_refuses_source_database_mode(self):
        with tempfile.TemporaryDirectory() as temporary:
            key = Path(temporary) / 'key'; key.write_text('fixture', encoding='utf-8')
            result = self.invoke('run', '--candidate-release', temporary, '--public-key', str(key), env={
                'NORN_M5_SIGNED_SHADOW_REHEARSAL': '1', 'NORN_REHEARSAL_BACKUP_ARTIFACT': '/private/backup',
                'NORN_REHEARSAL_BACKUP_PROOF': '/private/proof', 'NORN_REHEARSAL_SOURCE_DATABASE_URL': 'postgresql://live/norn'})
        self.assertEqual(result.returncode, 2)
        self.assertIn('without a source URL', result.stderr)

    def test_direct_handoff_rejects_missing_capability(self):
        result = self.invoke('shadow-start', env={'NORN_M5_SIGNED_SHADOW_REHEARSAL': '1'})
        self.assertEqual(result.returncode, 2)
        self.assertIn('capability is absent', result.stderr)

    def test_direct_handoff_rejects_live_port_before_launchctl(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            session = root / 'session'
            label = 'com.norn.m5.shadow.0123456789ab.0123456789abcdef'
            session.write_text(f'{label} 8800 token\n', encoding='utf-8')
            result = self.invoke('shadow-start', env={
                'NORN_M5_SIGNED_SHADOW_REHEARSAL': '1', 'NORN_M5_SHADOW_SESSION': str(session),
                'NORN_M5_SHADOW_LABEL': label, 'NORN_M5_SHADOW_PORT': '8800',
                'NORN_M5_SHADOW_HANDOFF_TOKEN': 'token',
                'NORN_REHEARSAL_PRIVATE_DATABASE_URL': 'postgresql://norn@/norn_private?host=%2Fprivate',
            })
        self.assertEqual(result.returncode, 2)
        self.assertIn('shadow port is invalid', result.stderr)

    def test_rendered_environment_preserves_socket_url_ampersands(self):
        with tempfile.TemporaryDirectory(prefix='norn-mini-private-copy.') as temporary:
            root = Path(temporary)
            socket = root / 'socket'; socket.mkdir(mode=0o700)
            output = root / 'runtime.env'
            url = f'postgresql://norn@/norn_private?host={socket}&port=55432&sslmode=disable'
            result = self.invoke('render-env', str(output), env={
                'NORN_REHEARSAL_PRIVATE_DATABASE_URL': url,
                'NORN_M5_SHADOW_PORT': '12345',
            })
            self.assertEqual(result.returncode, 0, result.stderr)
            readback = subprocess.run(
                ['/bin/bash', '-c', 'set -a; . "$1"; printf %s "$NORN_DATABASE_URL"', '_', str(output)],
                text=True, capture_output=True, check=True)
            self.assertEqual(readback.stdout, url)
            self.assertEqual(output.stat().st_mode & 0o777, 0o600)

    def test_rendered_environment_rejects_network_database_fallback(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / 'runtime.env'
            result = self.invoke('render-env', str(output), env={
                'NORN_REHEARSAL_PRIVATE_DATABASE_URL': 'postgresql://norn@127.0.0.1/norn_private?host=127.0.0.1&port=5432&sslmode=disable',
                'NORN_M5_SHADOW_PORT': '12345',
            })
            self.assertEqual(result.returncode, 1)
            self.assertIn('does not name the isolated socket target', result.stderr)
            self.assertFalse(output.exists())

    def test_forged_handoff_is_stopped_by_snapshot_signature_recheck(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary); root.chmod(0o700)
            label = 'com.norn.m5.shadow.0123456789ab.0123456789abcdef'
            session = root / 'session'; session.write_text(f'{label} 12345 token\n', encoding='utf-8'); session.chmod(0o600)
            release = root / ('a' * 40); release.mkdir()
            key = root / 'key'; key.write_text('fixture', encoding='utf-8')
            api = root / 'api'; api.write_text('fixture', encoding='utf-8')
            result = self.invoke('shadow-start', env={
                'NORN_M5_SIGNED_SHADOW_REHEARSAL': '1', 'NORN_M5_SHADOW_ROOT': str(root),
                'NORN_M5_SHADOW_SESSION': str(session), 'NORN_M5_SHADOW_LABEL': label,
                'NORN_M5_SHADOW_PORT': '12345', 'NORN_M5_SHADOW_HANDOFF_TOKEN': 'token',
                'NORN_REHEARSAL_PRIVATE_DATABASE_URL': 'postgresql://norn@/norn_private?host=%2Fprivate',
                'NORN_M5_SHADOW_PLIST': str(root / 'plist'), 'NORN_M5_SHADOW_ENV': str(root / 'env'),
                'NORN_M5_SHADOW_LAUNCHER': str(root / 'launcher'), 'NORN_M5_SHADOW_API': str(api),
                'NORN_M5_SHADOW_RELEASE': str(release), 'NORN_M5_SHADOW_SHA': 'a' * 40,
                'NORN_M5_SHADOW_MANIFEST': str(SCRIPT.parent / 'platform-release-manifest'), 'NORN_M5_SHADOW_ARTIFACT': str(SCRIPT.parent / 'platform-release-artifact'),
                'NORN_M5_SHADOW_PUBLIC_KEY': str(key)})
        self.assertEqual(result.returncode, 1)
        self.assertIn('platform release manifest error', result.stderr)

    def test_rejects_unsigned_copied_candidate_before_private_restore(self):
        sha = 'a' * 40
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            release = root / sha
            (release / 'bin').mkdir(parents=True)
            (release / 'release.env').write_text(f'NORN_RELEASE_SHA={sha}\nNORN_RELEASE_VERSION=v1.2.3-platform\n', encoding='utf-8')
            api = release / 'bin' / 'norn-api'; api.write_text('#!/bin/sh\n', encoding='utf-8'); api.chmod(0o755)
            key = root / 'key'; key.write_text('fixture', encoding='utf-8')
            receipt = root / 'receipt.json'
            result = self.invoke('run', '--candidate-release', str(release), '--public-key', str(key), env={
                'NORN_M5_SIGNED_SHADOW_REHEARSAL': '1', 'NORN_REHEARSAL_BACKUP_ARTIFACT': '/private/backup',
                'NORN_REHEARSAL_BACKUP_PROOF': '/private/proof', 'NORN_M5_SHADOW_RECEIPT': str(receipt)})
        self.assertEqual(result.returncode, 1)
        self.assertIn('platform release manifest error', result.stderr)

    def test_failed_sealed_candidate_copy_is_removed(self):
        sha = 'b' * 40
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            release = root / sha
            (release / 'bin').mkdir(parents=True)
            (release / 'release.env').write_text(f'NORN_RELEASE_SHA={sha}\nNORN_RELEASE_VERSION=v1.2.3-platform\n', encoding='utf-8')
            api = release / 'bin' / 'norn-api'; api.write_text('#!/bin/sh\n', encoding='utf-8'); api.chmod(0o500)
            key = root / 'key'; key.write_text('fixture', encoding='utf-8')
            receipt = root / 'receipt.json'
            scratch_parent = root / 'scratch'; scratch_parent.mkdir()
            (release / 'release.env').chmod(0o400)
            (release / 'bin').chmod(0o500)
            release.chmod(0o500)
            result = self.invoke('run', '--candidate-release', str(release), '--public-key', str(key), env={
                'TMPDIR': str(scratch_parent), 'NORN_M5_SIGNED_SHADOW_REHEARSAL': '1',
                'NORN_REHEARSAL_BACKUP_ARTIFACT': '/private/backup',
                'NORN_REHEARSAL_BACKUP_PROOF': '/private/proof', 'NORN_M5_SHADOW_RECEIPT': str(receipt)})
            self.assertEqual(result.returncode, 1)
            self.assertIn('platform release manifest error', result.stderr)
            self.assertEqual(list(scratch_parent.iterdir()), [])

    def test_has_unique_label_and_disabled_workers(self):
        text = SCRIPT.read_text(encoding='utf-8')
        self.assertIn('label="com.norn.m5.shadow.${sha:0:12}.$nonce"', text)
        self.assertIn("'NORN_SKIP_OPERATION_WORKER': 'true'", text)
        self.assertIn('shadow port is not solely owned by the shadow LaunchAgent', text)
        self.assertIn('shadow LaunchAgent failed to serve health', text)
        self.assertNotIn('NORN_M5_SHADOW_MANIFEST_HELPER', text)
        self.assertIn('NORN_M5_SHADOW_RECEIPT', text)
        self.assertNotIn('NORN_LAUNCH_LABEL=', text)

if __name__ == '__main__': unittest.main()
