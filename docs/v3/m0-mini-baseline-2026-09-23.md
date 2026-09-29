# Mini M0 read-only baseline checkpoint — 2026-09-23

This is a point-in-time inventory, not an upgrade fixture or M0 sign-off. It
does not authorize a release, provider action, or change to the running Mini.

## Provenance and observed counts

The Norn platform inventory script queried Mini over the existing authenticated
read-only API at 2026-09-23 19:29 UTC:

```sh
bash /Users/arti/.codex/skills/norn-platform/scripts/norn_inventory.sh \
  --remote mini --api http://localhost:8800 \
  --remote-sops-env /Users/0xadb/.config/norn/api.env.enc.json
```

The API reported `v2.20.0-platform-30-ga5da8ef` and current release SHA
`a5da8ef15d12e9eca7561e90b90d96f6dc652a21` (release record created
2026-09-22 13:34:42 CDT). Host status was `ok`. There were 27 app records:
22 with `deploy: true` and 5 with `deploy: false`. The app inventory reported
18 healthy and 9 not healthy, including inactive apps; 18 had Nomad status
`running`, one `dead`, and eight an empty status. The service manifest v2 had
44 process records and 26 endpoint records. Active control operations were
zero, active incidents were 13, and the Fleet node-pool list was empty. The
platform rollup warned that snapshot retention was over limit for two apps.

These numbers are API observations. They do not establish that every job,
allocation, route, volume, or database matches the declared app specification.
No credentials or application definitions were copied into this repository.

## M0 evidence still required

1. Pin the installed binary, source commit, schema migration ledger, launch
   configuration, and backup/recovery key provenance as one exact-version
   manifest. The release SHA above is an API record, not a binary hash or
   schema measurement.
2. Record app-to-job, endpoint/route, volume, secret-reference, and database
   ownership mappings; distinguish intentionally inactive apps from failed
   jobs. Recheck live Nomad/Consul and the route owners independently.
3. Measure control PostgreSQL table/index bytes, connection counts, and growth
   over an agreed interval. Set evidence, log, replay, and restore budgets from
   those measurements rather than draft targets.
4. Build a sanitized CI fixture. Rehearse a separate private restore with
   production egress and all workers, webhooks, cron, and provider mutation
   disabled. Verify source identity and rollback compatibility before M5.
5. Resolve the proposed ADRs, named owners, first app-engine support, and
   numerical acceptance budgets in a reviewed decision register.

M0 remains open until those items have reviewable evidence. This inventory can
be rerun, but its counts should not be treated as a durable migration manifest.
