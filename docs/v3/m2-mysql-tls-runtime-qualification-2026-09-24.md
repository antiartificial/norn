# M2 MySQL TLS runtime checkpoint — 2026-09-24

The resolver now admits MySQL application runtime delivery only for a CA-backed
`verify-full` binding whose server name equals the delivered endpoint host and
which has no client certificate. The InfraSpec must declare the four structured
connection variables and a CA file path variable. The pipeline stages the CA
in a revisioned Nomad Variable; the allocation template writes it under
`secrets/` with mode `0400`. Running-revision validation requires that CA
before function, cron, or rollback copying. `verify-ca`, client certificates,
and PostgreSQL TLS file declarations still fail closed on this path.

An opt-in test ran a generated WordPress Docker allocation against disposable
MySQL 8.4 with `require_secure_transport=ON` and a synthetic CA and server
certificate. Its `mysqli` probe enabled server-certificate verification,
observed a nonempty session `Ssl_cipher`, and matched the expected database
and user. The local WordPress image had digest
`sha256:8746e7e072e3e9e12144fb070928f81190639ef38e4d00cb959463d9ce2ab1d6`;
the local MySQL image had digest
`sha256:0744ee5ef89ce6ccfa13de3e579fe6b9e27f93dd70da9c06d2c908b1b193fb8d`.
The disposable job, variable, containers, listener, and certificates were
removed after the run.

`go test ./database ./model ./nomad ./pipeline -count=1` passed. A separate
deploy-acceptance test passed against disposable PostgreSQL 17.7 and a
read-only fake Nomad endpoint. These checks prove the delivery path and a
custom `mysqli` client using the delivered files. They do not yet prove stock
WordPress startup configured for TLS, client-certificate authentication,
`verify-ca` behavior, production MySQL backup/restore, or a release rollout.
