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
- Dispatch the workflow from `master`, enter its exact SHA as both
  `commit_sha` and `reviewed_workflow_sha`. The workflow rejects either value
  unless it matches the actual protected dispatch and workflow SHA.
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

The workflow publishes by digest only: it creates no mutable or discoverable
bootstrap tag. The handoff is always the digest-pinned form:

```
ghcr.io/antiartificial/hello-norn-mysql@sha256:...
```

The workflow retains `handoff.json` and `sbom.spdx.json` as a workflow artifact
for 30 days, and publishes provenance and SBOM attestations for that same
digest. Before accepting it, an independent reviewer records this reviewed
policy tuple outside `handoff.json`: the artifact digest, source SHA, and
workflow SHA. The handoff is discovery material only; it is not policy.

With those values in the shell, verify the two different predicates explicitly:

```sh
export REVIEWED_SOURCE_SHA=EXACT_40_CHARACTER_SOURCE_SHA
export REVIEWED_WORKFLOW_SHA=EXACT_40_CHARACTER_REVIEWED_WORKFLOW_SHA
artifact=ghcr.io/antiartificial/hello-norn-mysql@sha256:...
signer=antiartificial/norn/.github/workflows/hello-norn-mysql-bootstrap-image.yml

gh attestation verify "oci://$artifact" --repo antiartificial/norn \
  --signer-workflow "$signer" --signer-digest "$REVIEWED_WORKFLOW_SHA" \
  --source-digest "$REVIEWED_SOURCE_SHA" --source-ref refs/heads/master \
  --deny-self-hosted-runners

gh attestation verify "oci://$artifact" --repo antiartificial/norn \
  --signer-workflow "$signer" --signer-digest "$REVIEWED_WORKFLOW_SHA" \
  --source-digest "$REVIEWED_SOURCE_SHA" --source-ref refs/heads/master \
  --predicate-type https://spdx.dev/Document/v2.3 --deny-self-hosted-runners
```

Authenticate to GHCR before verification if required. The first command checks
SLSA provenance; the second checks the SPDX predicate. Both refuse
self-hosted runner evidence and bind the exact signer workflow, independently
reviewed workflow SHA, source SHA, and protected source ref.

## Controlled handoff

Copy only the reviewed tuple—digest-pinned artifact, source SHA, and workflow
SHA—into a reviewed Fleet configuration or direct-Nomad artifact input. Do not
derive that tuple from the handoff artifact, pass a tag, a secret, or a workflow
token. The normal staging/production release topology remains unchanged after
the Fleet exists; this exception solves only the first-image bootstrap and does
not create a qualification receipt.

If the pilot is abandoned, remove the GHCR package version through the
repository/package retention procedure only after recording its digest and
evidence in the pilot teardown record. This workflow never creates cloud
resources, so there is no infrastructure cleanup associated with it.
