# M8 platform release asset verification — 2026-10-02

This non-secret receipt records an independent, read-only verification of the
published Norn platform candidate. Its owner-only source receipt has SHA-256
`27a286e0531c7272f72ea8b862a9438d2112639b12f694ea352d539ddee68395`.

## Bound release

- Repository: `antiartificial/norn`
- Commit: `847c81741dd5b095d6833368ee8bb72dea6873d9`
- Tag: `platform-847c81741dd5b095d6833368ee8bb72dea6873d9`
- GitHub release: published 2026-10-01T22:40:58Z, neither draft nor
  prerelease, and reported immutable.
- Publication: [workflow run 36936156456](https://github.com/antiartificial/norn/actions/runs/36936156456)
  succeeded, including all four platform bundle jobs and publish.

## Independent verification result

At 2026-10-02T23:06:38Z, all 16 release assets were downloaded. Their
downloaded SHA-256 values matched GitHub's published asset digests. For each
platform, the manifest named the exact commit and tag above and its archive and
SPDX SBOM checksums matched the downloaded files. All four 64-byte Ed25519
manifest signatures passed with a public release trust anchor whose SHA-256 is
`05144c7584b005c95e95d05d5e5bff06676f456f37dbd99bf6b09849c6c2855c`.

| Platform | Archive SHA-256 | Archive bytes | Manifest SHA-256 |
| --- | --- | ---: | --- |
| darwin-amd64 | `7cde975fb31b06aff379b25770689b7c94559380f8864719cf49515bb13f9fb3` | 55,895,399 | `d9724cd23ae20224630b4f8abb3748fdb29a1b39ff1d8f5b37be3246745671ed` |
| darwin-arm64 | `9771adfe36b628d9e9e6db1b5813a73c1965359dfae9c2968db2cbe5698b7463` | 52,580,486 | `d479e505384d6b9bb00eeaf1b7f45dda9d7df0ca17dacc05219e5fb7afcd2fd2` |
| linux-amd64 | `c22aeac5d9e2a621a2c9f9dc32888c083fcb469b9c713346d745c2bc7581aaac` | 54,310,309 | `4a35804a0f69eb175fdff44aa2ca1ea0c986c6483d3f6ccc5462e1b2dfa09786` |
| linux-arm64 | `778a253e6c142270aa53363884ec49ed027436d6d2002c23bd5612c8c70b6991` | 49,910,991 | `a9b2d7772e922879c3d3e2649e83b1224c4cb82f8560d86996da32c4eba2fb13` |

The verifier used Homebrew OpenSSL 3.6.4 because the system LibreSSL lacks
Ed25519 support. No private key material was used.

A subsequent read-only Mini check found that its configured public release
key is byte-identical to this public trust anchor (113-byte PEM, SHA-256
`05144c7584b005c95e95d05d5e5bff06676f456f37dbd99bf6b09849c6c2855c`).
Mini's owner-only release configuration is mode `0600`, uses `require-signed`,
and has fetch and verify hooks configured. This checked the trust configuration;
it did not verify or import this candidate on Mini.

## Remaining boundary

This receipt establishes the integrity of published release material and the
Mini's configured public trust identity. It does not import or preflight the
candidate on Mini, promote it, run platform smoke or
host assurance, inspect a release listing, or rehearse rollback. Those steps,
along with the broader M8 gates, remain open.
