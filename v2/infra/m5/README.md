# Pinned M5 macOS bootstrap recipe

`upstream-vanilla-tahoe.pkr.hcl` is copied from
[`cirruslabs/macos-image-templates` at `d6a3774b296fb80aa42c2a4885d6eb364a909ffd`](https://github.com/cirruslabs/macos-image-templates/blob/d6a3774b296fb80aa42c2a4885d6eb364a909ffd/templates/vanilla-tahoe.pkr.hcl).
The upstream project is MIT licensed. The copied file's SHA-256 is
`2beef2f46e600e6f36ac1dc272077bb5154c1ae2ceea3cbacf1ea3462faa652c`.

The pinned Apple IPSW is
`https://updates.cdn-apple.com/2026SummerFCS/fullrestores/140-75212/A2A24B94-1FC1-45A3-93F7-C51B02AF1F4D/UniversalMac_26.6.2_25G83_Restore.ipsw`.
The official response length and verified local size are `19772231540` bytes.
Eight exact HTTP 206 ranges were assembled, then the full file was hashed
independently. Both hashes were
`885503b7f4b06609e9a512f2befd40f59730640a3f1233e3892d60affdd51c95`.
The owner-only local staging copy and acquisition evidence are under
`/private/tmp/norn-m5-ipsw-cache`; the redundant range parts were removed
after verification.

The dedicated M5 Ed25519 owner public key is pinned in every local guest
signature verifier at SHA-256
`ba038c8f9e5d43ff4b5c03ffb1ea7ca1e4e9127c98b5810ed0c6a5dbc1c67eeb`.
The private key is held outside the runner under the owner's 0700 work-staging
directory. The workflow variable containing the public-key path cannot select
a different trust anchor.

`mini-m5-packer-template` verifies that exact file and derives an M5-only
template. It pins the Tart plugin to 1.21.0, uses an already verified local
IPSW, binds a run-specific guest name and random bootstrap password, starts
the guest with Softnet, and omits upstream broad sudo, auto-login, and
Gatekeeper changes. The derived template still creates only a temporary
bootstrap account. The protected guest controller must harden that account
and prove network, GUI, and root-helper state before admitting retained inputs
or registering the M5 GitHub runner.

## Controller sequence

The protected supervisor uses a distinct `norn-m5-supervisor-01` runner and
`m5-mini-release-rehearsal` reviewer environment. `m5-local-guest-create.yml`
verifies an owner-signed intent bound to its GitHub run and source SHA, reserves
one guest name, creates the VM, and always attempts read-only reconciliation.
The durable `create-started` marker is written before Packer may affect Tart.
An ambiguous create outcome is held for reconciliation; a second create is
rejected. The guest is **created-unqualified** even when Packer succeeds.

`m5-local-guest-retire.yml` requires a different signed intent and protected
run. Its intent binds the exact guest name, create-marker SHA, created receipt
when present, and the live Tart configuration hash. It stops and deletes only a
guest whose configuration still matches, then reads Tart inventory to prove
absence. A failed or ambiguous create can be retired before runner registration.
A registered guest runner blocks this path until a separate protected
revocation controller proves that runner absent. A failed retire leaves a
durable marker and requires a protected reconciliation decision before another
delete attempt.

The guest qualification workflow runs on `norn-m5-isolated-01`, separate from
the supervisor. It emits an unsigned candidate containing the guest name,
create-marker SHA, source SHA, and hash of the isolation attestation. The owner
must review the runner-to-Tart-guest link and sign this candidate. The copied
state restore workflow verifies the signature and exact hashes before any
retained backup or release inputs are opened.
The candidate inherits the guest name and marker from the signed hardening
intent and requires the guest's own Ed25519 SSH host public key to match that
intent. Caller-supplied guest names cannot qualify another machine.

Before guest hardening, `m5-local-guest-hardening-admission.yml` verifies a
second signed intent against the create marker and receipt, Tart configuration,
the observed Ed25519 SSH host key, and a one-use guest runner public key. It is
read-only. The owner must bind the observed host key and Tart configuration to
the created guest before signing; SSH discovery alone is not authentication.

## Remaining guest hardening

No workflow may run the historical restore from the temporary Packer bootstrap
account. A protected bootstrap controller must remove or lock that account,
disable Screen Sharing and unneeded remote services, establish separate runner
and rehearsal accounts, install root-owned pinned firewall helpers inside the
guest, configure guest PF with the M5 anchor first, establish the rehearsal GUI
domain, and register a one-use guest runner. Qualification must independently
prove those controls. Softnet's host-side network restriction and the guest PF
window are separate boundaries. The guest runner and SSH host key must be bound
to the Tart guest identity before the owner signs the qualification candidate.
