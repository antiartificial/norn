# Signed platform asset verification

Observed at `2026-10-01T05:06:13Z` for [release `platform-8a291142677a7b00cc8606e61ac0a6952fff3e5c`](https://github.com/antiartificial/norn/releases/tag/platform-8a291142677a7b00cc8606e61ac0a6952fff3e5c). The GitHub release API reported `immutable: true`, target commit `8a291142677a7b00cc8606e61ac0a6952fff3e5c`, and 16 assets. The publishing [workflow run 36813207019](https://github.com/antiartificial/norn/actions/runs/36813207019) succeeded.

All four bundles were downloaded from that release into a private local cache and each passed `v2/scripts/platform-release-artifact verify --bundle-dir <platform-cache> --public-key <pinned-key>` from exact source `8a291142677a7b00cc8606e61ac0a6952fff3e5c`. The pinned Ed25519 public key SHA-256 was `05144c7584b005c95e95d05d5e5bff06676f456f37dbd99bf6b09849c6c2855c`. An independent local read then compared every asset byte count and SHA-256 to the release API. All 16 matched:

| Platform | Asset | Bytes | SHA-256 |
| --- | --- | ---: | --- |
| darwin-amd64 | `norn-platform-8a291142677a7b00cc8606e61ac0a6952fff3e5c-darwin-amd64.manifest.json` | 9704 | `363d3dcdd08b65e2f5e01b7023e6f011a97c4d1aaaae70367966b98207f13bc9` |
| darwin-amd64 | `norn-platform-8a291142677a7b00cc8606e61ac0a6952fff3e5c-darwin-amd64.manifest.sig` | 64 | `b2212c9dac93315a22b6e10349c0de89a655cb2a18e903d0baad771902fd1e92` |
| darwin-amd64 | `norn-platform-8a291142677a7b00cc8606e61ac0a6952fff3e5c-darwin-amd64.sbom.spdx.json` | 22557 | `3925cae7cbb733be29dbac223912687152d64166554f35f94d825761dcf888a6` |
| darwin-amd64 | `norn-platform-8a291142677a7b00cc8606e61ac0a6952fff3e5c-darwin-amd64.tar.gz` | 55882168 | `e42362b70594b298ef38a7b75ad55351c16ae3f343fc3291549f7d67d4f58c2a` |
| darwin-arm64 | `norn-platform-8a291142677a7b00cc8606e61ac0a6952fff3e5c-darwin-arm64.manifest.json` | 9704 | `f000efe2ddf7937627905efdb91a115eca64899183d0d02db48afcc72b594676` |
| darwin-arm64 | `norn-platform-8a291142677a7b00cc8606e61ac0a6952fff3e5c-darwin-arm64.manifest.sig` | 64 | `b34ce07595e031a162baeec07b7c94d0d3055b18e755822906268a43922d1196` |
| darwin-arm64 | `norn-platform-8a291142677a7b00cc8606e61ac0a6952fff3e5c-darwin-arm64.sbom.spdx.json` | 22557 | `eca0557865774f952d3ae0691b1b8300c2f4b865e732fdfdd8eed4ece0166201` |
| darwin-arm64 | `norn-platform-8a291142677a7b00cc8606e61ac0a6952fff3e5c-darwin-arm64.tar.gz` | 52566445 | `82c91b87bafef6b55064bfa474fce80e135cc6caffd70a6aade98ecca7aa9ed4` |
| linux-amd64 | `norn-platform-8a291142677a7b00cc8606e61ac0a6952fff3e5c-linux-amd64.manifest.json` | 9701 | `88faf2c170db6116457b4a998b699ac37da6389fc63fe64022e55d4837f6af9e` |
| linux-amd64 | `norn-platform-8a291142677a7b00cc8606e61ac0a6952fff3e5c-linux-amd64.manifest.sig` | 64 | `b69b5da597dc9946036909dbbe90e4953c77e912475af2cd116414c7fe4b1d78` |
| linux-amd64 | `norn-platform-8a291142677a7b00cc8606e61ac0a6952fff3e5c-linux-amd64.sbom.spdx.json` | 22554 | `4a67b77e2e7af8deacc89cd20093384874fce28e55050a33ab027a2ac7954ee0` |
| linux-amd64 | `norn-platform-8a291142677a7b00cc8606e61ac0a6952fff3e5c-linux-amd64.tar.gz` | 54296406 | `6b3032b935c6d4d736313409f0a4a440c5f8c58f8104fa31d72762b0fce3c76e` |
| linux-arm64 | `norn-platform-8a291142677a7b00cc8606e61ac0a6952fff3e5c-linux-arm64.manifest.json` | 9701 | `e1e025cfb966ebf12189d68869be1601e54b76f6321ec0f4180d083bad90d401` |
| linux-arm64 | `norn-platform-8a291142677a7b00cc8606e61ac0a6952fff3e5c-linux-arm64.manifest.sig` | 64 | `97325ce33f2b306d7370c69ba04a35cb73c7c3642e4156881f05864a49c836cc` |
| linux-arm64 | `norn-platform-8a291142677a7b00cc8606e61ac0a6952fff3e5c-linux-arm64.sbom.spdx.json` | 22554 | `18e85a1a69089185e644f2ff83691abf7026a62f3c16de23fd9be13d3c25daa2` |
| linux-arm64 | `norn-platform-8a291142677a7b00cc8606e61ac0a6952fff3e5c-linux-arm64.tar.gz` | 49895374 | `a7e472c3017b6b00922b38ec15bd217aaf8277196d011b79d4bddf9cdb850e3e` |

This proves the downloaded artifacts match the published immutable release and pass the pinned signature verifier. It does not prove installation, live Mini promotion, provider-backed Fleet operation, or end-to-end fitness. The private cache and verification public key remain outside the repository.
