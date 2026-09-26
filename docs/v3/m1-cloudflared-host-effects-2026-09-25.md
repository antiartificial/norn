# M1 local cloudflared effect conversion

The PostgreSQL Mini API now accepts `/forge`, `/teardown`, and
`/endpoints/toggle` as signed `app.cloudflared-mutate` operations. A claimed
worker reserves one host-wide effect before rewriting local config and
restarting cloudflared. The accepted payload binds the API host, config path,
the hash of the exact file bytes observed at admission, the intended config
hash, and the requested ingress edit. A worker on another host defers.

The local supervisor writes an owner-only receipt after `launchctl` reports a
successful restart. A recovered effect with that exact receipt can complete
without repeating the restart. If the API dies after the config write or a
restart returns ambiguously before the receipt, the effect stays unresolved
for operator review. It is never automatically replayed. A later ingress
mutation is blocked by the host-wide reservation until the outcome is
reconciled.

Receipt publication is now create-only: an exact repeated write reads and
accepts the original private file, while different bytes cannot replace it.
Receipt reads reject symlinks, hard links, non-private modes, wrong owners,
non-regular files, and oversized content. The focused local-driver test and
the PostgreSQL-backed cloudflared pipeline cases passed; exporting the receipt
with host recovery and testing actual Mini `launchctl` remain open.

An identical-key replay resolves the signed receipt before asking Consul or
Nomad for the current service. The HTTP route now also resolves an existing
receipt by the authenticated actor, app and key when the app spec or selected
endpoint has disappeared. Forge and teardown replays bind the original action;
toggle replay additionally binds the requested hostname. A mismatched action
or hostname conflicts rather than borrowing the old receipt. New mutations
still require the live spec and ordinary preflight. A disposable PostgreSQL 16
integration test passed these missing-spec replay and conflict cases; this
does not qualify live Mini launchctl behavior.

The CLI now prints the idempotency key before sending a mutation and reports
the queued operation ID. It waits for terminal status by default; `--wait=false`
returns after acceptance and `--timeout` bounds polling. The UI endpoint
toggle keeps its key across an unknown acceptance response, reports queued
status, and refreshes ingress only after the operation succeeds. A queued or
unresolved effect must not be presented as an already-applied ingress change.

This is a Mini host-local implementation, not a Fleet distributed ingress
implementation. The receipt directory is local to the same host as the
cloudflared config. Before release, rehearse actual `launchctl` behavior on a
private Mini copy, persist or export local receipts with host recovery, and
provide an operator procedure for reconciling ambiguous restart outcomes.
Test two API processes on one host and a wrong-host worker. The config writer
now preserves unknown top-level fields, ingress-rule fields, and comments from
the read YAML document while editing the known ingress entries. A local test
proves the accepted after-digest equals the published file after both an
existing service update and a new rule; the cloudflared pipeline and handler
cases passed against disposable PostgreSQL 16. Actual Mini config and
`launchctl` recovery remain unqualified. General etcd app route parity remains
outside this slice.

## Read-only Mini service identity — 2026-09-26

The live Mini inventory showed no active Norn operations and no Fleet pools.
Its cloudflared config was owner UID 501, mode `0600`, 1,393 bytes, and had
only the known `tunnel`, `credentials-file`, and `ingress` top-level keys. The
v3 receipt directory does not yet exist on that v2 host. The running service
is `gui/501/com.norn.cloudflared`; `gui/501/homebrew.mxcl.cloudflared` is not
registered. The Norn API process also runs as UID 501.

The v3 restart helper previously targeted that absent Homebrew label. It now
uses the current process UID and the managed `com.norn.cloudflared` label. A
PATH-scoped launchctl test passed with the exact `kickstart -k` arguments.
This was read-only host inspection and a local command-shape test; the live
service was not restarted, so actual Mini recovery remains open.

The live Mini checkout also validates a candidate with
`cloudflared --config <file> tunnel ingress validate` before replacing the
config, and loads the executable and launch label from host settings. The v3
branch had lost those safeguards. It now validates the owner-only temporary
file before the atomic rename, validates the published file before kickstart,
and uses the configured binary and launch label with the managed label as
default. A failed validator leaves the previous config intact in the local
test. Cloudflared/config tests and the PostgreSQL-backed two-process
pipeline/handler cases passed. This restores source compatibility with the
live host contract; it does not certify the v3 binary against Mini's actual
config or restart the live tunnel.

The helper now waits up to ten seconds for `launchctl print` to report that
the managed agent is running before it lets the supervisor write a success
receipt. A waiting agent fails the bounded local test. The cloudflared package
and the PostgreSQL-backed pipeline/handler cases passed with command-scoped
launchctl stubs. This proves launchd state handling, not tunnel connectivity
or public endpoint health; those remain part of the private Mini rehearsal.

On 2026-09-26, a read-only command on the Mini ran its installed
`/opt/homebrew/bin/cloudflared` 2026.8.2 against the live
`/Users/0xadb/.cloudflared/config.yml` with `tunnel ingress validate`; it
returned `OK`. This confirms the current v2 file passes the installed
validator. It does not validate a v3-generated candidate or exercise the
restart path. Norn PR #76 checks passed at `04d7ccf`; the live service was not
changed.

The same day, a synthetic owner-local fixture with a tunnel identifier,
credentials-file reference, existing ingress `originRequest` field, and
catch-all rule was read and changed by the v3 `ReadConfigSnapshot` →
`AddIngress` → `ApplyConfig` path. It updated one service, inserted one new
rule, and produced a 413-byte candidate that passed the installed Mini
cloudflared 2026.8.2 `tunnel ingress validate` command. The candidate was
transferred only to a private temporary file on the Mini and removed after
validation. It contained no production tunnel identifier or credentials.
This is parser compatibility evidence for that synthetic shape; the actual
Mini config rewrite, launchd restart, route health, and receipt recovery still
require a private rehearsal.
