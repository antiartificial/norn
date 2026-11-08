# V2 surface retirement inventory

Status: source audit, 2026-11-07. No runtime or test code was deleted.

## Findings

The V3 release workflow still builds the API and CLI from `v2/api` and
`v2/cli` (see `.github/workflows/platform-release.yml`, build steps 174–179;
`.github/workflows/v3-ci.yml`, Go setup and test steps 101–104). Those
directories are repository layout/module paths. They are not evidence of a
`/api/v2` HTTP namespace. The production control router registers its stable
versioned routes under `/api/v1` and also registers numerous older routes under
`/api` (`v2/api/main.go`, approximately lines 731–819 and 3099 onward). A
source search of the active router found no general `/api/v2` namespace.

Several `/api` routes are still called by shipped source in this repository.
Examples include:

- CLI reads `/api/apps`, `/api/ops/platform`, and `/api/operations/active`, and
  posts `/api/apps/{id}/deploy`, `/preflight`, and `/rollback`
  (`v2/cli/api/client.go`, lines 1051–1056, 1089–1119, and 1672–1704).
- The CLI still uses `/api/events`, `/api/platform/releases`, app logs and
  exec, secrets, scaling, and snapshot endpoints (same client, lines 1251–1284,
  1609–1617, 1714 onward, and 2162 onward).
- The V3 UI still calls `/api/apps`, `/api/access/patterns`, and
  `/api/apps/{id}/{action}` (`v2/ui/src/runtime/AppRuntime.tsx`, lines 80–112,
  143–195). It mixes those with `/api/v1` calls.

These are actual in-tree consumers, not merely historical route declarations.
The current API version therefore has a compatibility surface that is broader
than `/api/v1`. Deleting the old router wholesale would break current CLI/UI
features. Migrate consumers route-by-route, and preserve parity for idempotency,
response bodies, auth scopes, websocket/log behavior, and recovery semantics.

## Separate protocol: offline effect grants

`/api/v1/fleet/offline/attempts/{planID}/{attemptID}/effect-grants/v2` is a
versioned subprotocol under the `/api/v1` control API, not an `/api/v2` API.
Its handler and durable store implement a distinct V2 grant/effect journal
(`v2/api/etcd_fleet_offline_effect_v2_http.go`, lines 19–51;
`v2/api/etcdstore/v3_fleet_offline_effect_v2.go`). The route tests protect the
default-off evidence-source gate, one-use dispatch authorization, safe
readback, and completion semantics
(`v2/api/etcd_fleet_offline_effect_v2_http_test.go`, lines 67–115).

There is a current consumer in the separate `norn-fleet` repository:
`scripts/work_app03_norn_device_transport.py`, lines 240–245, posts and reads
the `effect-grants/v2` route. The surrounding app03 orchestration also performs
dispatch/recovery through those versioned grant endpoints. This surface is not
safe to remove until that protected runner and all saved/recoverable grant
records have a compatible replacement.

## What the audit does not justify deleting

- Do not remove all `v2/api` or `v2/cli` files: the V3 CI and release workflows
  build those modules.
- Do not delete unversioned `/api` handlers while the checked-in CLI/UI still
  call them.
- Do not delete code/tests because their identifiers contain `V2`, `legacy`,
  or a module path `v2`. Some V2 schemas protect stored intent, journal,
  signature, grant, or migration compatibility.
- Do not remove `/effect-grants/v2` based only on the newer V3 controller
  architecture. The external `norn-fleet` caller is still wired to it.

## Safe retirement sequence

1. Generate a method/path inventory from the production router and compare it
   to CLI, UI, mobile/native clients, workflows, and the protected
   `norn-fleet` repository. Mark each route as current, alias, migration-only,
   or dead with source evidence.
2. For each unversioned route to retire, add or identify an equivalent
   `/api/v1` contract, migrate every caller, and add consumer tests for the
   method, body, idempotency behavior, authorization, and response shape.
3. Keep the old route as a compatibility alias while observing caller usage.
   Only remove it after supported clients have moved and external usage has
   been checked; retain read-only/recovery routes as long as old operations
   or journals can still require them.
4. For `effect-grants/v2`, introduce and qualify an explicit successor
   protocol, update and release the protected `norn-fleet` runner, test
   interrupted and lost-response recovery across both versions, then wait for
   active grants to reach terminal/expired states before disabling V2 writes.
   Keep V2 read/recovery support until persisted V2 records are no longer
   within the supported recovery window.
5. Delete only tests whose behavior is replaced at the new contract boundary.
   Keep migration fixtures, crash/replay tests, old-record readers, and
   compatibility rejection tests until their corresponding stored or
   installed version is out of support.

No route or test deletion is recommended from this audit alone. The immediate
useful cleanup is an endpoint-by-endpoint caller map and a staged migration
plan, not a bulk removal based on “V2” names.
