# Managed MySQL staging catalog activation

## Current status (2026-11-12)

The managed app source is publishable and has `deploy: true` at
`norn-fleet-mysql.tkmkt-dev.com`. Fleet promoted the catalog in protected run
[42312866244](https://github.com/antiartificial/norn-fleet/actions/runs/42312866244)
and read it back in
[42341905690](https://github.com/antiartificial/norn-fleet/actions/runs/42341905690).
The hostname resolves to `159.89.243.220`.

The managed app has not been released. A current TLS probe returns the Traefik
default certificate for this hostname. The protected TLS workflow has no run,
`NORN_MANAGED_MYSQL_STAGING_RELEASE_ENABLED` is `false`, and the release
workflow has no `workflow_dispatch` run. There is no Norn-owned version A or B
deployment and no rollback proof. Keep this stateful app track separate from
the first stateless controller-admitted DO canary.

## Activation sequence

The catalog promotion and DNS steps below are complete. Use this sequence for
the remaining activation work. Recheck every live binding at effect time.

On 2 November 2026, `tkmkt-dev.com` delegated to `ns1`, `ns2`, and
`ns3.digitalocean.com`. The work DigitalOcean context identified load balancer
`e9902566-3412-43a6-a4f8-9a4a03652439` at `159.89.243.220`, with TLS passthrough
to ingress-01 (`610012099`) and ingress-02 (`610012096`). Cloudflare is not
authoritative for this zone. The `norn-pilot` context belongs to another zone
and must not be used.

The protected DNS executor created the A record. The current DNS answer is
`159.89.243.220`. Verify A, AAAA, CNAME, and CAA against all three
authoritative nameservers before certificate issuance. If CAA exists, prove it
permits the selected ACME issuer.

The local Certbot installation has `dns-cloudflare` but no DigitalOcean
authenticator. The Cloudflare plugin cannot prove this domain. Use a protected
Certbot DNS-01 controller with `--manual-auth-hook` and
`--manual-cleanup-hook` that invoke scoped `doctl` TXT create/delete. Persist
the exact TXT record ID, poll authoritative
`_acme-challenge.norn-fleet-mysql.tkmkt-dev.com`, and clean only that ID.
Publish the issued fullchain and key as owner-only protected material. Do not
expose them in workflow logs. Install and read back a valid certificate for
this hostname on both ingress nodes through a protected executor. The latest
SNI probe returned `CN=TRAEFIK DEFAULT CERT`; the TLS gate remains open.
Confirm both SNI endpoints through the load balancer and, if private node
access is available, directly on each ingress. Verify that no router already
owns this host. Keep the managed release flag off.

The protected catalog publisher, promotion, and three-control readback have
completed. Review the publisher artifact, InfraSpec SHA, archive SHA, and
provenance JSON from the protected publisher run when tracing that release.
Catalog selection does not deploy the app.

Before the first Norn-owned release, install and verify the `staging-app-runtime` zero-service database profile and app deploy worker, the four revision-job-scoped Nomad Variable keys for the managed job, MySQL TLS identity, and the protected CI release binding. Keep the web database identity limited to application data reads and writes; schema migration is a separate identity. Run the protected staging release for version A, verify durable operation success, two healthy allocations, public `/version` and `/route-proof`, and a database-backed record roundtrip. A second source-only pilot app commit becomes version B while the same InfraSpec, database and route remain selected. Measure continuity during B, then use the protected staging rollback lane to restore the exact A deployment. Do not use the older direct Nomad job as an A/B release target.
