# Mini Nomad database target join — 2026-09-26

Read-only Nomad API inspection on Mini found 37 base jobs and 41 database-related
environment fields. The inspection ran on Mini and emitted only key names,
consumer job names, URL scheme families, and opaque target groups. It did not
print or copy connection strings, hosts, database names, passwords, or template
contents. Periodic child jobs were excluded. This is a declared-job inventory,
not a successful connection or backup test.

| Declared target family | Current consumers | Boundary indicated by the job definition |
| --- | --- | --- |
| PostgreSQL URL | `contextdb`, `field-harbor` and its three periodic jobs, `its-alive-api`, `like-trove` and its periodic jobs, `mail-mcp`, `signal-sideband`, `turnkey-offer-intake`, `vigil-gateway` (two URL fields), `watchtower` | Ten distinct URL target identities after grouping by scheme, hostname, port and database path. Each hostname is written as a non-loopback name. This does not prove physical location or independent storage. |
| Host and separate database fields | `mail-indexer`, `signal-sideband` | Both name the same host in `DB_HOST`, while other fields select credentials/database. Host equality does not mean the same database. |
| HTTP service URL | `mail-mcp` | `CONTEXTDB_URL` points to an HTTP API, not a direct SQL connection. Its backing store belongs to the ContextDB service boundary. |

The ten PostgreSQL URL identities are one each for `contextdb`,
`field-harbor`, `its-alive-api`, `like-trove`, `mail-mcp`,
`signal-sideband`, `turnkey-offer-intake`, and `watchtower`, plus two
configured URLs for `vigil-gateway`. No two of those identities were equal
under the inspected URL fields. The `like-trove` periodic jobs all referred
to the same configured URL identity as its service job; the three
`field-harbor` periodic jobs likewise matched its service job.

## Release boundary

- The Mini control database backup covers `norn_v2`, not these application
  targets. The v2→v3 control upgrade must preserve each application's existing
  connection binding and leave its job, route, and data ownership unchanged.
- Owner review must classify whether each URL is managed PostgreSQL, a local
  service reached by hostname, or another backing arrangement; record backup
  owner, recoverable point, and restore test for each. The two `vigil-gateway`
  URLs need an explicit distinction between its application store and its
  Norn-facing database reference.
- `mail-indexer` and `signal-sideband` need database-name-aware classification;
  their shared host alone is insufficient. The `mail-mcp` HTTP ContextDB
  reference needs a service-level dependency and restore boundary.
- Reinspect the exact running allocation environments at the maintenance
  window. A Nomad job definition can differ from a running allocation, and an
  environment value can be overridden after the declared snapshot.

No application database was read, connected to, dumped, or changed. This
narrows M0 ownership mapping but does not close M0 or M5.
