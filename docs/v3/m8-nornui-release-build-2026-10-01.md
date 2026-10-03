# NornUI M8 private pilot-build checkpoint

Status: development-signed local build and fixture launch, 2026-10-01. No
published, distributed, or installed NornUI release was produced by this check.

The clean NornUI checkout at
`3252851ec74647e73a8d84935101694d6606de3a` (tree
`20c148652e218485122336b22859720f2c87affe`) is the head of
[NornUI PR #20](https://github.com/antiartificial/NornUI/pull/20). Its exact-head
[macOS CI run 36827216694](https://github.com/antiartificial/NornUI/actions/runs/36827216694)
passed build and unit tests. A separate local built test bundle completed
237 passed tests with zero failures and one opt-in cross-client test skipped when its
loopback server was absent. The earlier fixture-activation test covers an
expiring saved device credential; fixture mode refuses managed-credential
rotation.

An arm64 `Release` build using the project's default signing settings passed.
After copying the app to the owner-only `pilot261001a/nornui-release-pr20-head`
directory, `codesign --verify --deep --strict` passed. The signature reports
`Apple Development: Aaron Barton (27S6KHZ4L5)`, TeamIdentifier
`AC65XL27P3`, and CDHash `4a1e29b13a8f8280ef6831846da1bb2791c39d49`.
The signed executable SHA-256 is
`e59395dd2aca5df99eee01ef91ab87d3000be0d1de058288591a0481dc96c694`.
An owner-only HMAC receipt verified against the detached clean source checkout,
test summary, and copied bundle with Fleet's existing private NornUI
receipt verifier. Its SHA-256 is
`0727470615d88dc5ccc242cbfa0c7b42c113429c52108b77385d2d8ba8aa9ca3`;
the receipt's bundle-manifest SHA-256 is
`ed6f5c886b7aac288e7a725372de383446c15902feff24b94323080b79b9d5a5`.
The receipt and its key remain in owner-only local storage. The earlier `b59afc6`
private build and receipt remain valid historical evidence, but are superseded
for the current PR head.

The earlier `b59afc6` copied app was launched as a separate instance with `NORN_UI_FIXTURES=1`.
The native window displayed Overview with “Explore Mode. Sample data.” The
pilot instance was then closed while the previously installed NornUI process
remained running. This is a fixture launch, not a connection to Mini or Fleet.

The new opt-in cross-client test ran the real Norn capabilities handler and
bearer middleware on an ephemeral loopback port, then used NornUI's actual
`NornClient` to decode its response. The Xcode result bundle recorded one
passed Swift test, zero failures and zero skips; the wrong bearer returned 401.
The local Go test and NornUI test are included in their respective open PRs.
This verifies capability negotiation for the tested signed-private staging
configuration, not a live Mini or Fleet connection.

Next proof is a protected published or installed NornUI build, a real
install/launch against the signed Norn candidate, and remaining client parity.
The local Apple Development signature and HMAC receipt do not establish
public distribution, notarization, live control-plane behavior, or M8 sign-off.
