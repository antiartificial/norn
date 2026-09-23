# Isolated browser retry fixture

This fixture serves the built UI and a deterministic synthetic API from the
same IPv4 loopback authority. It has no proxy or outbound HTTP client. Unknown
`/api/*` requests return `404` and are recorded in `GET /__fixture/state`, so a
missing fixture route cannot fall through to a real Norn API.
Mutating requests with a present `Origin` header must match the fixture's exact
loopback origin, and encoded path traversal is rejected before URL resolution.

The scenarios cover:

- preflight response loss, a later `401`, and terminal same-key replay;
- partial deploy-group acceptance followed by same-parent-key recovery; and
- a running deploy whose first durable-operation poll returns `503` before
  succeeding.

Use `/apps` for the preflight and deploy scenarios. The synthetic app has
deployments enabled. Use `/platform/releases` for the `core` deploy group.

After source review, build and launch it with:

```sh
pnpm build
pnpm fixture:browser -- --port 0
```

The process prints one JSON line containing its `http://127.0.0.1:<port>` URL.
Use only that printed URL. The fixture rejects any other `Host` authority and
never reads API tokens, credentials, service URLs, or provider configuration
from the environment. Stop it with `Ctrl-C`.

The browser exercise can inspect deterministic counters at
`GET /__fixture/state` or reset them with `POST /__fixture/reset`. The fixture
test itself uses a temporary static directory and an ephemeral loopback port:

```sh
pnpm test:browser-fixture
```
