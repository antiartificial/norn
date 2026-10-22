# Fleet platform release genesis

The currently signed `40237c11fdeb6d7b8753e1874355c533a84de76d` release predates the platform-release intent API. It cannot accept a controller attempt or a genesis import. The first controller-driven upgrade must target a later signed release whose source contains the `fleet-platform-release-intent-v1` capability.

The external management controller owns the entire first attempt in its durable journal. It must verify the signed old and new release identities, preserve the exact control roster and state/host-key digests, stage and promote one member at a time, and check the active etcd API over the pinned HTTPS route. Before it signs a successful completion receipt, the active API's `/api/v1/capabilities` response must advertise `fleet-platform-release-intent-v1`, and each control member must be observed healthy at the new signed source. A source commit or local test alone is insufficient.

The staging intent's three `controlRoster` entries contain exactly `name` and
`privateIp`. Each private IP must be unique and inside `10.43.0.0/20`. The
same ordered roster is bound by the intent and genesis-completion digests; the
management controller uses these private IPs with pinned SSH host keys. A
control-node tailnet address is not part of this contract.

After completion, an enrolled non-CI platform operator may import the signed genesis intent, signed completion receipt, and ordered checkpoints at `POST /api/v1/fleet/platform-releases/{name}/genesis-import`. Norn pins separate Ed25519 PKIX public keys for the operator intent and management completion signatures. It verifies exact canonical bytes, immutable release bindings, final capability, control roster digest, and checkpoint count/hash, then stores an immutable import record. The import creates no new rollout attempt and cannot replace a normal platform-release intent. The key paths are `NORN_FLEET_PLATFORM_GENESIS_INTENT_PUBLIC_KEY_FILE` and `NORN_FLEET_PLATFORM_GENESIS_COMPLETION_PUBLIC_KEY_FILE`; the API and import route are default off until `NORN_FLEET_PLATFORM_RELEASE_INTENT_ENABLED=true` and both valid, distinct pins are configured.

Subsequent platform releases use the API's typed intent and enrolled `fleet:execute` device attempt lease. The recovery routes remain closed. A future recovery path needs a separately pinned supervisor signature over proof that the old owner stopped, fresh signed member-state observation, operator authorization, and expiry of all old remote promote grants before an etcd CAS may advance the fence epoch. An expired or failed attempt holds until that proof exists.
