# NornUI M8 local Release-build checkpoint

Status: private development-signed pilot-build evidence, 2026-10-01. No
published, distributed, or installed NornUI release was produced by this check.

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
The unsigned output remains under `/private/tmp/nornui-m8-release-63a-unsigned`
and is not the retained pilot artifact. Strict `codesign --verify --deep
--strict` failed for that bundle.

The project configures signing team `AC65XL27P3`. The installed older NornUI
app demonstrates that an Apple Development certificate named for
`27S6KHZ4L5` can still produce a signature whose TeamIdentifier is
`AC65XL27P3`; the certificate name is not the signing-team readback. An
automatic signing attempt with a manually specified certificate failed
Xcode's conflicting-settings check.
An automatic `Apple Development` attempt using the available team failed with
"No signing certificate Mac Development found". A manual-signing attempt
remained in `codesign` for more than 90 seconds without completing; private-key
access may have been waiting, but that was not verified. It was interrupted,
and the resulting bundle failed signature verification. These failed attempts
do not prove a distribution authority.

The same clean source then passed an arm64 `Release` build using the project's
default signing settings. After copying the app to the owner-only
`pilot261001a/nornui-release` directory, strict code-sign verification passed.
Its authority is `Apple Development: Aaron Barton (27S6KHZ4L5)`, its
TeamIdentifier is `AC65XL27P3`, and its CDHash is
`f1cdf709d3de99b1edba70b00b55b53e1d1c1e2a`. The signed executable
SHA-256 is
`0edd06f83bfdaffa5de122ac154f6485ca101a933f680cde6b0f441754a41481`.
An owner-only HMAC receipt, verified by Fleet's existing private NornUI
receipt verifier against the detached clean Git checkout and copied bundle,
has SHA-256
`96e1992a00646c778613f6f6385e0ac687fe395c7a0c97140f5d396ded6c7039`.
The receipt's bundle manifest SHA-256 is
`e68d558884ce84fa239f65582ca9475ac7ab5704a21915a8dd760fc5dd0e02cc`.
The receipt and its key stay in owner-only local storage; neither is a
public-release signature.

Next proof is a protected released or installed NornUI build, a real
install/launch against the signed Norn candidate, and remaining client parity.
The local Apple Development signature and HMAC receipt do not establish
distribution, notarization, installed identity, or M8 sign-off.
