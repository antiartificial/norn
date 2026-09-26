# Normal etcd Fleet router qualification

Date: 2026-09-25

## Disposable evidence

`TestEtcdFleetRouterPGFreeGitHubConfiguredProcess` builds and starts the
ordinary `norn-api` binary against a disposable local etcd v3.5.17 instance.
The test runs the same normal-router path twice:

1. with `NORN_DATABASE_URL` absent; and
2. with `postgres://poisoned.invalid:1/never-open`.

Before each startup it writes the required published etcd bootstrap marker and
an operator managed token. The binary is configured with a repository-scoped
GitHub App configuration whose API base is a closed loopback address. The test
does not call the dispatch route, so it makes no GitHub or provider mutation.

Over HTTP, it verifies health, GitHub-configured capabilities, and that the
OIDC exchange endpoint is registered. It submits a signed capacity plan with
the managed operator token, binds a synthetic reviewed dispatch directly in
the disposable control store, then uses a durable managed CI token to create a
runner attempt, append a signed successful reconciliation checkpoint, and
advance the attempt. The advance succeeds only after the checkpoint admission.

The exact command and result were:

```text
NORN_TEST_ETCD_ENDPOINTS=http://127.0.0.1:<random-port> \
  go test . -run '^TestEtcdFleetRouterPGFreeGitHubConfiguredProcess$' -count=1 -v

PASS
  absent PostgreSQL DSN
  poisoned PostgreSQL DSN
```

The same process test passed again on 2026-09-26 at Norn PR #76 head
`8b7c4b5`, against a disposable digest-pinned etcd v3.5.17 container. Both
the absent-DSN and poisoned-DSN subtests passed. The container was removed on
exit. This rerun checks that the later signed-admission and lost-commit changes
still permit the ordinary PG-free Fleet API startup and router path; it does
not expand the single-member fixture's topology or authentication scope.

## Remaining gaps

This proves normal startup and routing against a disposable unauthenticated
single-member etcd fixture. It does not exercise the live GitHub Actions OIDC
assertion exchange because the production verifier intentionally accepts only
the fixed GitHub issuer JWKS over HTTPS. The CI token is constructed with the
same Norn managed-token format and recorded in etcd to test the downstream
router authorization and durable runner contract.

It also does not call GitHub App dispatch, provider APIs, or a protected Fleet
workflow. TLS/RBAC three-member, outage, restore, and provider-safe workflow
qualification remain separate M3 release gates.
