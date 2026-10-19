# Offline Fleet prechange observation contract

The staging-only runner checkpoint endpoint requires `receipt`, the complete
read-only JSON emitted by Fleet's `offline_contraction_effect_time.py`, plus
`evidenceSHA256` of the exact JSON bytes sent. Norn rejects a stale receipt or
one whose source commit, OpenTofu 1.10.6 saved-plan/effect digests, versioned
Spaces state identity (bucket, key, version, lineage, serial, raw hash), work
DigitalOcean account, app-03 droplet identity, or two-replica Nomad placement
differs from the one-use reservation. It stores the receipt hash, schema,
observation time, and expiry with the attempt/fence checkpoint. Its restore
reader rejects a corrupted summary or mismatched append-only event.

This is a **non-effect** checkpoint. The Fleet receipt is runner-produced
read-only evidence, not an independently signed attestation, and it does not
contain the attempt, target fence, CPU/memory headroom, or service-health
observations. Norn binds the checkpoint to the attempt and live fence in its
etcd transaction, but no provider credential or effect route consumes this
checkpoint yet. A future effect gate must require the structured summary,
reobserve state/provider/Nomad from pinned origins immediately before acting,
independently prove headroom and service health, and reject expiry or a changed
fence. Legacy internal digest-only test checkpoints are not effect-qualified.
