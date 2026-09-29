# V3 client and control-plane compatibility matrix — source checkpoint

This is a source review for draft Norn PR #77, not M8 qualification. Pin the
merged Norn SHA, signed release, installed Mini/Fleet versions and exact NornUI
revision before signing the release matrix. A capability response is an offer
of routes, not evidence that a protected runtime completed an operation.

| Control plane | Client path at this source checkpoint | Advertised or routed behavior | Release evidence still required |
| --- | --- | --- | --- |
| Existing Mini, PostgreSQL backend | Norn CLI and bundled web UI from the same release | Normal PostgreSQL API remains the default. The candidate starts active with automatic schema migration unless passive/check is explicitly selected. | Exact signed candidate, private Mini copy upgrade/rollback, then live preservation and smoke. |
| Existing Mini, PostgreSQL backend | Function callers, including `norn invoke` | `/api/apps/{id}/invoke` returns 503 until the private signed Function V3 runtime is configured. The legacy inline Nomad handler is not a fallback. | Inventory callers and qualify the claimed function path before advertising function parity. |
| Empty Fleet, etcd backend | CLI Fleet inventory, plans, and protected runner | The narrow normal router exposes inventory, plan receipts, exact operation reads and, when the GitHub App is configured, PR/dispatch and runner attempt/reconciliation routes. Other `/api` routes return 501. | Exact released binary on protected hosts; OIDC, plan, attempt, checkpoint and recovery through the protected pilot with a poisoned control-PG URL. |
| Empty Fleet, etcd backend | NornUI | The capability document now includes the required `auth` object, configured environment/profile, `fleet-v1`, inventory/plans and `fleet-only` authority marker. NornUI uses that marker to skip app, host, release and event reads that the narrow router does not serve. Device enrollment and Fleet GitHub status are not advertised by this router; a manually supplied scoped token and the CLI/protected workflow remain the operator paths. | Decode and refresh against the actual signed etcd runtime; verify the UI shows Fleet inventory/plans without implying absent app or host state. |
| Empty Fleet, etcd backend | App release preview | The release deployment route and claimed worker exist only when their explicit configuration is complete. General app mutations and full PostgreSQL API parity are outside the empty-Fleet bootstrap contract. | Protected ingress/public-path and deployment recovery qualifications before positive traffic or app adoption. |

The initial [release contract](execution-milestones.md#release-contract) requires
the Mini's PostgreSQL upgrade and a fresh three-member etcd Fleet with
independent application databases. It does not require general PG-to-etcd
control conversion. The later representative app move remains a separate
qualified operation. Keep unsupported etcd routes closed while collecting
the specific evidence above.
