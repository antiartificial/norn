# NornUI M8 private pilot-build checkpoint

Status: development-signed local build and fixture launch, 2026-10-01. No
published, distributed, or installed NornUI release was produced by this check.

The clean NornUI checkout at
`b59afc6c38d01629bfd2be262b03f8d7e79e4df6` (tree
`7c8ba13300bf0f67cbdc9e46f646269a7b73d3e9`) is the head of
[NornUI PR #20](https://github.com/antiartificial/NornUI/pull/20). Its exact-head
[macOS CI run 36825272264](https://github.com/antiartificial/NornUI/actions/runs/36825272264)
passed build and unit tests. A separate local built test bundle completed
236/236 unit tests, including fixture activation with an expiring saved device
credential. Fixture mode now refuses to enter managed-credential rotation.

An arm64 `Release` build using the project's default signing settings passed.
After copying the app to the owner-only `pilot261001a/nornui-release-b59`
directory, `codesign --verify --deep --strict` passed. The signature reports
`Apple Development: Aaron Barton (27S6KHZ4L5)`, TeamIdentifier
`AC65XL27P3`, and CDHash `398b907d32b5ad6062c62cf090a83684b6b66249`.
The signed executable SHA-256 is
`0e1354a4ccabd50ad2b4fa872432ab5dc8c78e4268696e97c98dc648258114a1`.
An owner-only HMAC receipt verified against the detached clean source checkout,
236-test summary, and copied bundle with Fleet's existing private NornUI
receipt verifier. Its SHA-256 is
`c38ca57b3cc2c0a38a8966bb382463c6168c269aea34e746099b71e88af6fbbf`;
the receipt's bundle-manifest SHA-256 is
`f6cb60df5feab46d2f40fe01b39805d412f3c8c8d3918de817eb00584c14d1b3`.
The receipt and its key remain in owner-only local storage. The earlier `63a0b5e`
private build and receipt are superseded for this proposed NornUI head.

The copied app was launched as a separate instance with `NORN_UI_FIXTURES=1`.
The native window displayed Overview with “Explore Mode. Sample data.” The
pilot instance was then closed while the previously installed NornUI process
remained running. This is a fixture launch, not a connection to Mini or Fleet.

Next proof is a protected published or installed NornUI build, a real
install/launch against the signed Norn candidate, and remaining client parity.
The local Apple Development signature and HMAC receipt do not establish
public distribution, notarization, live control-plane behavior, or M8 sign-off.
