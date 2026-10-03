# Mini signed 8a shadow rehearsal

Between 2026-10-01T04:57Z and the passing receipt at 05:05:33Z, Mini's live API
was still the legacy signed release
`a5da8ef15d12e9eca7561e90b90d96f6dc652a21`. An isolated source worktree
at exact Norn commit `8a291142677a7b00cc8606e61ac0a6952fff3e5c` used
the checked protected-backup and signed-shadow helpers. The candidate release
on Mini verified against the pinned Ed25519 public key and reported
`v2.20.0-platform-58-g8a291142`.

The protected environment loader supplied the control database URL and audit
signing key without printing their values. A fresh, owner-only on-host backup
was 14,675,415 bytes with SHA-256
`4823d3a4e3ac0252c108554b8234088692a78d3c511c95156ef9821d99c74f3a`.
The rehearsal verified its proof, restored those bytes only into a private
Unix-socket PostgreSQL 17.7 instance, completed two migrate-only passes, and
ran a separate passive/check LaunchAgent. It reported 28 original tables,
275,168 original rows, matching primary-key and full-row fingerprints,
schema ledger versions 1–47, minimum reader 5, minimum writer 31, passive
health passed, and cleanup verified. The exclusive mode-`0600` sanitized
shadow receipt at
`/Users/0xadb/.local/state/norn/m5-shadow-8a291142-20261001-Qjzvnw/shadow-rehearsal-receipt-v3.json`
has SHA-256 `f3203b069f25fc08ceefeed2ba93c8e4b6737f4eabdde5fb3af643a9e925c432`,
candidate SHA `8a291142677a7b00cc8606e61ac0a6952fff3e5c`, and exit code 0.
The mode-`0600` sanitized result transcript at the same path with filename
`shadow-result-v3.txt` has SHA-256
`98b6f398cdf72b245b1e6e60f6a5cbe937f63166c31bf50a603e070448bdf4ec`;
it records the detailed table, row, ledger, fingerprint, version, health and
cleanup fields above. Both are retained privately on Mini. A capture retry
with a restrictive process umask failed release-file mode verification before
the private restore; the corrected run produced this passing receipt.
The protected artifact, proof, URL, and key remain private on Mini.

After the rehearsal, the live `127.0.0.1:8800` API health check passed and
still returned `v2.20.0-platform-30-ga5da8ef`; no candidate listener remained
on `127.0.0.1:18800`. This proves the signed candidate's private-copy schema
and passive runtime path against actual Mini data. It does not prove a live
service switch, installed-binary preservation across that switch, old-binary
rollback after schema migration, disaster recovery, or Fleet fitness. The
maintenance fence, fresh transition-bound backup/shadow receipt, operator
window, and post-switch verification remain separate.
