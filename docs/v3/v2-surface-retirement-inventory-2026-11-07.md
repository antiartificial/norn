# V2 surface retirement inventory

Status: source audit with first caller migration, refreshed 2026-11-08. No
legacy handler or compatibility test was deleted.

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
The app-list caller has started moving: CLI `ListApps` and the UI app-list
query now call `/api/v1/apps`. Both routes currently invoke the same
`ListApps` handler, and V1 is present in the published OpenAPI contract. A
focused CLI test and UI assertion protect the V1 path. The legacy GET alias
remains registered because external use has not been measured and the same
`/api/apps` prefix still has live action, logs, snapshot, and other consumers.

Operation history has now followed the same migration pattern. The V1 API
provides `/api/v1/operations` and `/api/v1/operations/active`; CLI and UI
callers use those routes, and capability discovery plus OpenAPI advertise them.
The `/api/operations` aliases remain registered for unknown external clients.
The `active` collection is explicitly classified as a general `api:read`
request; only an exact operation ID is eligible for the existing handler-bound
scoped read.

Remaining examples include:

- CLI reads `/api/ops/platform` and `/api/v1/operations/active`, and
  posts `/api/apps/{id}/deploy`, `/preflight`, and `/rollback`
  (`v2/cli/api/client.go`, `PlatformOps`, `ListOperations`, `Deploy`,
  `Preflight`, and `Rollback`).
- The CLI still uses `/api/events`, `/api/platform/releases`, app logs and
  exec, secrets, scaling, and snapshot endpoints (same client methods).
- The V3 UI still calls `/api/access/patterns` and
  `/api/apps/{id}/{action}` (`v2/ui/src/runtime/AppRuntime.tsx` and app/action
  components under `v2/ui/src`). It mixes those with `/api/v1` calls.

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

No route or test deletion is recommended from this audit alone. The next
retirement slices should migrate one V1-covered method/path at a time, compare
consumer payload/auth behavior, and then re-scan the production router and all
clients. Do not remove the `/api/apps` compatibility route until external use
and remaining `/api/apps/{id}/...` callers are resolved.
