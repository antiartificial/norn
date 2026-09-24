# M2 MySQL TLS health adapter — 2026-09-24

The MySQL adapter now supports verified TLS for a local health probe. It
resolves the catalog's CA and optional client certificate/key references via
the private `SecretSource`, creates a per-connector TLS configuration, and
never permits plaintext fallback. `verify-ca` checks the certificate chain
and server-auth purpose; `verify-full` also checks the declared server name.
Errors returned to API callers do not include certificate or credential bytes.

Focused tests used a TLS server to prove an accepted CA, refusal of an
unrelated CA, exact hostname verification, and refusal of a wrong hostname.
A disposable `mysql:8.4` server with its generated CA then passed the real
MySQL identity probe over `verify-ca`. The same test confirmed that the TLS
session does not expose ordinary WordPress runtime components. The disposable
server and copied CA file were removed. `go test ./model ./database ./nomad
./pipeline -count=1` passed.

Resolver acceptance continues to reject a MySQL TLS `runtime` capability.
Only declared `health` can resolve to this TLS adapter: Nomad allocation trust
material, client certificate placement, and application-specific TLS settings
are not yet implemented or qualified. MySQL snapshot, restore and migration
capabilities also remain disabled. A managed service and client-certificate
integration test is still required before extending this beyond the local
health boundary.
