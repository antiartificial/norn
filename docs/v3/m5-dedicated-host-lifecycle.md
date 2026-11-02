# M5 dedicated host lifecycle contract

The historical Mini rehearsal uses a disposable macOS ARM64 host. The active
Mini, Xenon, and Fleet Macs are excluded. GitHub workflow jobs are execution
steps on that host; they are not the host provisioner. The provisioner must be
a separate protected controller with a durable one-use journal and a provider
adapter bound to one account and region.

## Admission

Before a provider create, require an owner-signed intent containing the exact
Norn protected `master` commit, GitHub run ID, provider account, region, host
type, macOS image ID, provider resource tag, owner-generated SSH host key fingerprint,
maximum quoted cost, and `retireBy` time. The intent must bind a fresh
provider inventory showing no resource with the run tag. The controller must
verify the quote against the provider's current account/region price response,
including minimum allocation and storage/network charges. Reject an expired
intent, a quote above its cap, or a missing/ambiguous price response.

The `v2/scripts/mini-m5-host-admission` helper verifies the owner's signature
over the intent bytes and checks exact run/source/provider scope, a fresh empty
inventory, a fresh quote, the maximum cost through `retireBy` including the
provider's minimum allocation, and expiration. The quote
and inventory must come from the selected provider's authenticated adapter;
the helper cannot establish their provenance on its own. It uses Homebrew
OpenSSL 3 for Ed25519 verification; the host bootstrap must pin that binary.
It performs no
provider or journal mutation. No create lane may call it with a hand-written
quote or inventory.

Use an exclusive, fsynced journal entry before the first provider request. A
lost provider response leaves the journal held; recovery looks up the exact
run tag and provider ID and cannot issue a second create while the outcome is
unknown. The controller records provider request ID, resource ID, immutable
image/type, and fresh inventory before enabling any runner. Only a provider
adapter with a separately scoped create/read/delete credential may perform
these calls. GitHub's M5 environment requires reviewer `antiartificial`.

## Qualification and rehearsal

Bootstrap the new host through an authenticated SSH host key fixed by the
signed intent. The host has no production credentials or live service routes.
Create separate `norn-m5-runner` and `norn-m5-rehearsal` accounts and a real
`gui/<rehearsal UID>` launchd domain. Install the reviewed root PF window and
isolation checker bytes, restricted sudoers entries, PostgreSQL tools, both
signed releases, and the pinned public key. The rehearsal user alone owns the
retained historical backup, proof, and old shadow receipt. Their transfer is
encrypted, private, and hash verified against the signed input manifest. Do
not stage those bytes in GitHub artifacts or runner checkout.

Register one GitHub runner named `norn-m5-isolated-01`, with only the
`norn-m5-isolated` label and this repository's scope. Record its runner ID and
host ID together in the journal. Dispatch `m5-isolated-host-qualification.yml`
at the exact protected commit. Verify its terminal success, runner ID, host
attestation artifact digest, and current GUI/PF state before dispatching
`m5-historical-copied-state.yml`. The restore workflow repeats the host gates
at effect time. A completed shadow receipt is evidence for one copied-state
restore; full M5 additionally requires the disposable upgrade/recovery
controller, identity comparison, and separate API/application traffic proof.

## Retirement

Fence and revoke the exact GitHub runner before deleting the host. Preserve
the terminal receipt and nonsecret attestation outside the host. Delete the
provider resource by recorded ID, then obtain a fresh scoped inventory proving
the run tag and ID absent. Release any dedicated host only after the provider's
minimum allocation. Keep the journal held if runner revocation, deletion, or
absence readback is uncertain. The retirement controller can retry reads and
idempotent deletion of the same recorded ID; it cannot create a replacement.

No provider adapter is selected yet. The AWS EC2 Mac path requires a Dedicated
Host with a 24-hour minimum allocation, account quota, AMI, subnet, and an
account/region quote. MacStadium requires account inventory and a portal/API
quote. These values must be supplied and verified before an executable create
lane is enabled.

## Local macOS guest candidate

The current Apple Silicon workstation can supervise a disposable macOS guest
without changing its own PF rules. Read-only inventory on 2026-11-02 showed
32 GiB RAM, about 130 GiB free disk, and UTM installed, but UTM's CLI has no
create command and the only UTM guest is HaikuOS. No macOS guest, local IPSW,
or Norn M5 runner exists. The local path therefore needs its own protected
guest provisioner; the active host cannot serve as the rehearsal runner.

Use Tart and its Packer plugin as the candidate controller adapter. Pin the
Tart/Packer/plugin binaries and the official Apple IPSW URL and content hash;
the current matching IPSW is
`https://updates.cdn-apple.com/2026SummerFCS/fullrestores/140-75212/A2A24B94-1FC1-45A3-93F7-C51B02AF1F4D/UniversalMac_26.6.2_25G83_Restore.ipsw`.
Its HEAD response reported 19,772,231,540 bytes. Reserve a 50 GiB guest disk,
8 GiB RAM, four CPUs, and a host free-space floor before creation. The
controller must run from an exact signed protected invocation, create a
one-use journal before `tart create`/Packer, and reconcile the exact guest name
and disk path after any uncertain result. It must not let Tart prune unrelated
VMs or caches. No host directory is mounted into the guest.

The published Cirrus Packer recipe automates Setup Assistant from this IPSW,
but its default `admin/admin`, broad sudo, and disabled Gatekeeper are only a
bootstrap state. Before transferring historical inputs or registering a
GitHub runner, the controller must replace the bootstrap identity, install
the restricted accounts/sudoers/root helpers, and prove the exact guest GUI
domain. Start the guest with Tart Softnet and attest that the guest cannot
reach host Mini/Xenon/Fleet addresses or private production routes while it
can reach GitHub for runner registration. Keep the PF deny window wholly
inside the guest. Qualification and restore must run on the guest runner;
revoke that runner, stop/delete only the journaled guest, and prove both guest
and runner absent at retirement. Do not use the workstation's existing
external-Mac finalizer runner for M5 effects.

The `mini-m5-local-guest-controller` source now verifies a signed exact local
intent, pinned Tart executable and fully downloaded IPSW, host capacity, and
the absence of the exact run VM in Tart's machine-readable inventory. Its
`reserve` mode creates an owner-only, exclusive, fsynced one-use journal and
retains intent/signature/key digests. It has no create, qualify, or destroy
mode yet. The journal is intentionally not a license to run `tart` or Packer
manually.

This is a candidate, not a passing host qualification. Packer's automated
Setup Assistant route needs a reviewed template, pinned Packer/plugin/Softnet
binaries, and a guest hardening/readback controller before the first VM effect.

The downloaded archives match their publisher SHA-256 values: Tart 2.41.1
`e680a734…`, Softnet 0.25.0 `5a2e8ef7…`, Packer 1.16.2 Darwin arm64
`3d7e4df8…`, and Tart Packer plugin 1.21.0 Darwin arm64 `4a34ca87…`.
The extracted Tart executable is `6d3a4554…`, which the read-only controller
pins. `mini-m5-packer-template` derives an M5 bootstrap from the exact Cirrus
template at commit `d6a3774b…` and SHA-256 `2beef2f4…`. It uses a random
bootstrap password, local verified IPSW, and Softnet; it removes the upstream
passwordless sudo, auto-login, and Gatekeeper disabling. The bootstrap guest
still requires hardened account/GUI/SSH readback before retained inputs.
