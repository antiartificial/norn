---
title: Hello Norn MySQL first-image bootstrap
description: One-time, artifact-only image publication before a pilot Fleet exists.
---

# Hello Norn MySQL first-image bootstrap

`Bootstrap Hello Norn MySQL image` is a deliberately narrow, manually
dispatched workflow for the circular first-image problem: a Fleet needs an
immutable workload image during initial configuration, while the normal release
path expects an already reachable staging control plane.

It is an artifact publisher only. It builds the selected source, publishes one
digest-pinned GHCR artifact, and attaches independently verifiable GitHub build
provenance and SPDX evidence. It cannot invoke a control plane, join a private
network, submit a workload, stage a release, qualify a release, or promote a
release. It has no environment or API secret contract.

## Preconditions

- `master` is protected.
- The exact, lowercase 40-character SHA has already been merged to `master`.
- Dispatch the workflow from `master`, and enter the same SHA as `commit_sha`.
- The repository is public. This bootstrap uses GitHub's public OIDC
  attestation service.

The workflow rejects a private or internal repository. Do not add a Norn API,
Tailscale connection, or a local signing key to work around that rejection. A
private/internal variant requires an independently operated attestation signer
with its own reviewed key-management and verification contract; none is
configured by this workflow.

## What it publishes

The fixed `pilot-go` contract is intentionally repeated in the workflow:

- `golang@sha256:484ef6066fa69acb059fdfeda7ba2b8f7391f2ef6abc6f9b8411e669ebd56466`
- `gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab`
- `APP_VERSION=<exact selected SHA>`

The visible `bootstrap-<SHA>` tag is refused if it already exists, so a retry
cannot silently move it. The handoff is always the digest-pinned form:

```
ghcr.io/antiartificial/hello-norn-mysql@sha256:...
```

The workflow retains `handoff.json` and `sbom.spdx.json` as a workflow artifact
for 30 days, and publishes provenance and SBOM attestations for that same
digest. An independent reviewer can use the command recorded in `handoff.json`:

```sh
gh attestation verify --repo antiartificial/norn \
  ghcr.io/antiartificial/hello-norn-mysql@sha256:...
```

Inspect the returned statement before accepting the digest: its source material
must be the selected SHA and its workflow identity must be the bootstrap
workflow in this repository.

## Controlled handoff

Copy only the digest-pinned artifact and source SHA into a reviewed Fleet
configuration or direct-Nomad artifact input. Do not pass the tag, a secret, or
the workflow token. The normal staging/production release topology remains
unchanged after the Fleet exists; this exception solves only the first-image
bootstrap and does not create a qualification receipt.

If the pilot is abandoned, remove the GHCR package version through the
repository/package retention procedure only after recording its digest and
evidence in the pilot teardown record. This workflow never creates cloud
resources, so there is no infrastructure cleanup associated with it.
