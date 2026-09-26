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
Test two API processes on one host and a wrong-host worker. The current parser
rewrites known config fields and should be hardened to preserve unknown YAML
fields/comments. General etcd app route parity remains outside this slice.
