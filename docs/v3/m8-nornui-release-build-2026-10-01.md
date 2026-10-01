# NornUI M8 local Release-build checkpoint

Status: engineering evidence only, 2026-10-01. No signed, published, or
installed NornUI release was produced by this check.

The clean NornUI checkout at
`63a0b5e9a884fc09d9a491d145b4620acfe7f84e` (tree
`a4f7a51873fe9f1837ad43d162207cbedec02b5d`) is the head of
[NornUI PR #20](https://github.com/antiartificial/NornUI/pull/20). Its exact-head
[macOS CI run 36821630818](https://github.com/antiartificial/NornUI/actions/runs/36821630818)
passed build and unit tests. A separate local built test bundle completed
235/235 unit tests.

An arm64 `Release` configuration build from that checkout passed with
`CODE_SIGNING_ALLOWED=NO`. The app reports bundle ID
`com.antiartificial.NornUI` and short version `1.0`; its executable SHA-256 is
`2feeb2789cfc01fd4476dacc1319e43b669aa6326053d6b94c56edc662348b46`.
The output remains under `/private/tmp/nornui-m8-release-63a-unsigned` and is
not a retained release artifact. Strict `codesign --verify --deep --strict`
failed for this bundle.

The project configures signing team `AC65XL27P3`. The available named Apple
Development identities on this Mac report team `27S6KHZ4L5`; no matching
project-team identity was observed. An automatic signing attempt with a
manually specified certificate failed Xcode's conflicting-settings check.
An automatic `Apple Development` attempt using the available team failed with
"No signing certificate Mac Development found". A manual-signing attempt
remained in `codesign` for more than 90 seconds without completing; private-key
access may have been waiting, but that was not verified. It was interrupted,
and the resulting bundle failed signature verification.
None of these attempts proves a usable signature or distribution authority.

Next proof is a clean exact-source Release build signed by an authorized
non-ad-hoc identity, strict signature and team readback, immutable bundle
digest/receipt, and a real install/launch against the signed Norn candidate.
Full NornUI client parity and M8 sign-off remain separate gates.
