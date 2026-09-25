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
MySQL identity probe over `verify-ca`. At this checkpoint, the TLS session
did not expose ordinary WordPress runtime components. The disposable
server and copied CA file were removed. `go test ./model ./database ./nomad
./pipeline -count=1` passed.

Resolver acceptance now admits the narrow CA-only `verify-full` runtime shape
described in the [runtime checkpoint](m2-mysql-tls-runtime-qualification-2026-09-24.md).
Client-certificate placement and stock application TLS startup remain
unqualified. MySQL snapshot, restore and migration
capabilities also remain disabled. Client-certificate and stock application
startup tests are required before extending the runtime qualification.
