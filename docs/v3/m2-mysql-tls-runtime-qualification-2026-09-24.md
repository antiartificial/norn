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

## WordPress `db.php` hook qualification — 2026-09-24

WordPress 6.8.2's `require_wp_db()` loads `wp-content/db.php` after
`class-wpdb.php` and before it creates the global `$wpdb`. Norn's small,
version-pinned qualification drop-in subclasses that loaded `wpdb` class. It
sets `MYSQLI_OPT_SSL_VERIFY_SERVER_CERT`, supplies the allocation-private CA
file to `mysqli_ssl_set`, and calls `mysqli_real_connect` with
`MYSQLI_CLIENT_SSL`; therefore the connection uses MySQLi's normal chain and
server-name verification rather than an application-side allowlist.

The opt-in Nomad test rendered both the Norn revisioned CA template and the
static drop-in into a disposable allocation, copied the drop-in to
`wp-content/db.php` before the official image entrypoint, and exercised the
normal HTTP installation route. Against disposable MySQL 8.4 with
`require_secure_transport=ON` and a certificate valid for
`host.docker.internal`, the correct CA returned the installation page. The
WordPress image was `wordpress:6.8.2-php8.3-apache` at digest
`sha256:09ac1315368f234db7559e4f9dcca3178a5efc6f2193b88289252abe18551522`;
the tested drop-in SHA-256 was
`498b2a8b79d93172bfedb5fc17cd0ec25160262a3daddba37f76107d2a1fa6d1`.
A second allocation with an unrelated CA returned WordPress's “Error
establishing a database connection” page. A third allocation used the trusted
CA but a reachable endpoint name outside the server certificate SAN. Its
startup first completed a credential-free TCP connection to that same host and
port, then WordPress returned that same failure page. The uniquely named jobs
and their Nomad variables were removed by the test.

This proves the supported WordPress hook plus CA and hostname-mismatch
negative controls. It does not yet prove an operator-declared InfraSpec can
select the startup wrapper, that persistent WordPress content storage
preserves the managed drop-in, or
that all WordPress images use the same entrypoint and MySQLi implementation.
The database resolver's MySQL verified-runtime gate remains closed. Enabling
it requires reviewed product wiring for those boundaries plus repeated
allocation qualification against the exact supported image digest.

## Production adapter follow-up

`docker buildx imagetools inspect docker.io/library/wordpress:6.8.2-php8.3-apache`
also reports `sha256:09ac1315368f234db7559e4f9dcca3178a5efc6f2193b88289252abe18551522`
as the pullable OCI index digest, correcting the earlier assumption that it
was only a local image ID. The product InfraSpec now has a narrowly validated
`wordpress-verified-tls/v1` startup adapter for that exact image, the named
primary MySQL runtime, and a writable persistent `wp-content` volume. Nomad
translation embeds the same `db.php` bytes tested above, checks the pinned
SHA-256 before installation, refuses an altered existing drop-in, and uses a
same-directory rename for initial installation. Generated-job and local
startup-script tests pass.

## Generated adapter allocation — 2026-09-25

`TestWordPressVerifiedTLSStartupAdapterPersistentContentInNomad` exercised the
product-generated job in disposable local Nomad 2.0.7 with MySQL 8.4 TLS.
With the trusted CA, the actual WordPress installation route loaded. With an
unrelated CA, the actual WordPress route rejected the database connection.
After a distinct replacement allocation, the same persistent `wp-content`
sentinel remained readable. The test uses a fresh host port on replacement to
avoid a Docker port-release race after Nomad stops the first allocation.
Focused model/Nomad tests passed, and the disposable jobs and fixtures were
removed. The generated startup script also needed `$target` in place of
`${target}` because Nomad interpreted the braced form while validating the job.

This qualifies the generated adapter's trusted-CA, wrong-CA, and persistent
replacement paths on the pinned WordPress image. A subsequent opt-in generated
allocation test also used a trusted CA with a reachable hostname outside the
server certificate SAN. TCP reached MySQL from the same allocation, while
WordPress rejected the database connection. The disposable jobs, variables,
Nomad agent, and MySQL fixture were removed. Mini rollback with production
storage and backup, and release rollout, remain open.

## Narrow runtime admission — 2026-09-25

The catalog resolver now accepts MySQL runtime only with `verify-full`, an
endpoint-matching server name, a CA reference, and no client certificate.
The named-target binder checks that the consuming app is the exact pinned
`wordpress-verified-tls/v1` shape at both acceptance and execution. Generic
consumers, mutable images, missing persistent content, `verify-ca`, and
client-certificate shapes remain refused. PostgreSQL-backed binder tests
passed for the qualified declaration and rejection after spec drift; the
database/model/Nomad/pipeline package tests passed locally. The resolver's
transport result alone is not permission to deliver an unqualified client.

Production artifact admission now has one code-bound upstream prebuilt case:
the exact qualified WordPress adapter and OCI index digest may pass without a
`norn.git.sha` image signature, which an app source repository cannot
truthfully assert for Docker Official WordPress. Registry digest lookup and
vulnerability scanning still run. Missing content, changed image, generic
consumer, altered runtime declaration, and bound release artifacts retain the
normal signature policy. The trust assertion is embedded in the signed Norn
binary; a separately attested Norn mirror is later distribution hardening.

`TestClaimedWordPressVerifiedTLSDeployInNomad` then passed against disposable
PostgreSQL, MySQL 8.4 TLS, Nomad 2.0.7 and Consul. Signed `app.deploy`
acceptance led to a claimed worker, clean Git source checkpoint, exact pinned
image admission, verified MySQL target probe, revisioned private CA variable,
and a generated WordPress allocation with passing HTTP health. The terminal
operation succeeded. A second signed deployment changed only the CA
reference to an unrelated CA; it failed at Norn's verify-full target probe
before staging a new Nomad variable, leaving the trusted revision current.
The earlier generated-allocation wrong-CA and hostname tests remain the
application-path negative controls. Jobs and disposable services were removed.

The prebuilt exception does not cover bound release artifact rollback/import;
that path needs a spec-bound receipt or a qualified mirror. Mini rollback,
MySQL backup/restore, and release rollout remain separate gates.
