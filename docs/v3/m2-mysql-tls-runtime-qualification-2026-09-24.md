# M2 MySQL TLS runtime checkpoint — 2026-09-24

The delivery contract supports a CA-backed `verify-full` binding whose server
name equals the delivered endpoint host and which has no client certificate.
The InfraSpec declares four structured connection variables and a CA file path
variable. The pipeline can stage the CA in a revisioned Nomad Variable; the
allocation template writes it under `secrets/` with mode `0400`.
Running-revision validation requires that CA before function, cron, or rollback
copying. **Resolver acceptance for MySQL TLS application runtime remains
closed** pending proof that a supported application client actually verifies
the certificate. `verify-ca`, client certificates, and PostgreSQL TLS file
declarations also remain gated on this path.

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
deploy-acceptance test exercised the CA declaration against disposable
PostgreSQL 17.7 and a read-only fake Nomad endpoint. These checks prove the
delivery shape and a custom `mysqli` client using the files.

A stock WordPress 6.8.2 PHP 8.3 Apache startup also reached the installation
page and negotiated TLS when given `MYSQL_CLIENT_FLAGS=MYSQLI_CLIENT_SSL` and
`SSL_CERT_FILE`. A **wrong-CA negative control did the same**: HTTP returned
200 and the WordPress `wpdb` session still showed a nonempty `Ssl_cipher`.
That configuration therefore proves encryption only, not CA or hostname
verification. Stock WordPress must reject the wrong CA through a supported
client hook before its MySQL TLS runtime can be qualified. Client-certificate
authentication, `verify-ca` behavior, production MySQL backup/restore, and a
release rollout also remain open.
