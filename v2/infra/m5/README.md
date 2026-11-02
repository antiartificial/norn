# Pinned M5 macOS bootstrap recipe

`upstream-vanilla-tahoe.pkr.hcl` is copied from
[`cirruslabs/macos-image-templates` at `d6a3774b296fb80aa42c2a4885d6eb364a909ffd`](https://github.com/cirruslabs/macos-image-templates/blob/d6a3774b296fb80aa42c2a4885d6eb364a909ffd/templates/vanilla-tahoe.pkr.hcl).
The upstream project is MIT licensed. The copied file's SHA-256 is
`2beef2f46e600e6f36ac1dc272077bb5154c1ae2ceea3cbacf1ea3462faa652c`.

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
run. Its intent binds the exact guest name and create-marker SHA. It stops and
deletes that guest only, then reads Tart inventory to prove absence. A failed
or ambiguous create can be retired without a qualification receipt. A failed
retire leaves a durable marker and requires a protected reconciliation decision
before another delete attempt.

The guest qualification workflow runs on `norn-m5-isolated-01`, separate from
the supervisor. It emits an unsigned candidate containing the guest name,
create-marker SHA, source SHA, and hash of the isolation attestation. The owner
must review the runner-to-Tart-guest link and sign this candidate. The copied
state restore workflow verifies the signature and exact hashes before any
retained backup or release inputs are opened.

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
