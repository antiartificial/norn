# Offline Fleet prechange observation contract

The staging-only runner checkpoint endpoint requires `receipt`, the complete
read-only JSON emitted by Fleet's `offline_contraction_effect_time.py`, plus
`evidenceSHA256` of the exact JSON bytes sent. Norn rejects a stale receipt or
one whose source commit, OpenTofu 1.10.6 saved-plan/effect digests, versioned
Spaces state identity (bucket, key, version, lineage, serial, raw hash), work
DigitalOcean account or app-03 droplet identity differs from the one-use
reservation. It also checks that the plan and freshness observations name one
candidate digest, the reviewed provider archive/version and VPC, eight unique
active-looking node IDs, and two distinct Nomad allocations on app-01/app-02.
Those latter assertions come from the submitted receipt. A v1 reservation
cannot bind survivor IDs. A new v2 reservation requires the exact candidate
SHA-256 and all eight unique provider node IDs in both the binding and the
owner-signed v2 approval; the attempt must inherit them. Only when the receipt
matches that signed set does the summary mark `providerTopologyVerified: true`.
This means **matched to the signed reservation**, not independently observed
provider truth. Norn stores the receipt hash, schema,
observation time, and expiry with the attempt/fence checkpoint. Its restore
reader rejects a corrupted summary or mismatched append-only event.

This is a **non-effect** checkpoint. The Fleet receipt is runner-produced
read-only evidence, not an independently signed attestation. Old v1
reservations remain readable but yield `providerTopologyVerified: false`;
arbitrary, unique survivor IDs can still be submitted under v1. Even v2
owner acknowledgement does not prove the live provider state.
The receipt does not
contain the attempt, target fence, CPU/memory headroom, or service-health
observations. Norn binds the checkpoint to the attempt and live fence in its
etcd transaction, but no provider credential or effect route consumes this
checkpoint yet. A future effect gate must require the structured summary,
reobserve state/provider/Nomad from pinned origins immediately before acting,
independently prove headroom and service health, and reject expiry or a changed
fence. Legacy internal digest-only test checkpoints are not effect-qualified.

## Staged effect-time observer composition

The unmounted `observerclient.CompositeEffectSource` combines four fresh,
independent reads: protected Fleet source proof, work-account DigitalOcean
inventory, version-pinned state bytes and foreign-lock absence, and all-namespace
Nomad/Consul workload health. It accepts only the signed binding and exact
attempt/challenge identity, completes within 30 seconds and within the first
minute of the challenge, and journals all four bounded transcripts before a
signed effect attestation can be submitted. The work-staging app-03 Nomad node
ID `fb10cefb-d416-363c-89b6-cb3a8247899a` is pinned in source because the
signed dispatch binding contains its DigitalOcean ID but no Nomad ID. A node
replacement must fail closed until an owner reviews and updates that pin,
rebuilds the observer, and creates a new binding and challenge. A caller cannot
select the replacement ID. This source is not mounted or deployed; live
credential provisioning, target trust, writer fence, and terminal proof remain
separate gates.
