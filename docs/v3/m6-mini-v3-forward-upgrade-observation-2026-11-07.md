# Mini in-place upgrade observation — 2026-11-07

Status: **not upgraded**. The Mini remains healthy on signed release
`3602e7e3d5fe5c0ef333b4041580d48ec15b24c5`; no database migration or release
promotion occurred during this attempt.

This is a historical pre-upgrade observation for the failed candidate below.
It was superseded by the successful independent Mini V3 adoption and
controller recovery recorded at the top of [LAUNCH-STATE.md](LAUNCH-STATE.md)
on 2026-11-07, with a live read-only refresh on 2026-11-08. Do not use this
status as the current Mini release state.

## Live evidence

- Mini API health returned HTTP 200. Its schema is migration 49, reader floor 5,
  writer floor 32.
- Candidate `03934cc2e9fdbd9b57cf09270f8c7884aada552f` passed internal manifest
  and external signature verification. It requires schema migration 51.
- Candidate preflight stopped before mutation because the database is at 49 and
  the candidate requires at least 51.
- The normal `platform-upgrade upgrade` path stopped before migration and
  promotion because catalog 51 raises the writer floor from 32 to 33. It
  explicitly cannot prove every writer, including `norn-host-agent`, is stopped
  or compatible. Do not bypass this guard or run SQL migrations by hand.
- Seven queued `fleet.github.apply-dispatch` receipts were present. Platform
  drain logic preserves queued Fleet receipts and excludes them from
  Mini-affecting active work.
- The previous recovered-forward generation was finalized with its exact signed
  controller binding. It is generation
  `gen-41bde512aa418c5977998d1b28ca7dd9`, transition
  `mini-v3-20261013-3602e7e3`, source
  `3da50fe3a3c9f37d64e24c03a7dcfea94153da58`, candidate
  `3602e7e3d5fe5c0ef333b4041580d48ec15b24c5`. The Mini remained on `3602…`
  and its API health returned HTTP 200 afterward.
- The repair note names controller `4b518b2c733dee8ce947d6ecb5c5232c971fede2`,
  but that installed helper accepts only `prepare` and `verify`. The generation's
  signed API plist record is bound to controller
  `aa41d0b153cc04317c7216a419c95d52ec876aef`; that exact signed helper supports
  the `run` recovery action and completed finalization. Future repair must use
  the controller SHA already bound to the signed plist record.

## Next safe route

Keep the Mini on `3602…` until a controller-managed forward transition can prove
all database writers are quiesced, execute schema 51 once, promote the exact
signed candidate, restore the host agent, and verify API/schema and preservation
afterward. The ordinary restart lane is not that path.

Prepare a fresh protected Mini database backup and a candidate-`03934…`
signed shadow receipt. No matching shadow receipt for `03934…` was found in the
inspected Mini config tree. The existing
`mini-recovered-forward-transition` wrapper needed a supported new-generation
handoff from the completed generation to exact source `3602…`. The local
controller now supports that handoff by retaining the completed signed
generation pointer and binding the successor to its immutable journal,
evidence, and preservation receipt. The generation-preparation regression
suite covers a completed-predecessor rollover. This source change is not yet
in a signed release or deployed to the Mini. Do not mutate the active-generation
pointer or reuse the old source/candidate evidence manually.

This handoff only unblocks generation preparation. The Mini still needs fresh
candidate-specific backup/shadow evidence and an implementation that proves
every database writer, including `norn-host-agent`, is compatible or fenced
before schema 51 begins. It does not yet prove a low-interruption online
upgrade.

This candidate-specific shadow is an isolated database restore and passive
candidate runtime check; it does not require another full macOS IPSW guest. No
full OS guest was created or removed during this attempt. At inspection, the
workstation had about 85 GiB free, the Mini about 76 GiB, and `tart` was not
installed on the workstation.

## Separate Tart guest attempt

The protected guest-create run
[`42297051412`](https://github.com/antiartificial/norn/actions/runs/42297051412)
later passed intent verification and reservation, then failed while Packer was
building the Tart guest. Protected reconciliation reported
`create-outcome-ambiguous` with an empty Tart inventory; that is not a terminal
absence receipt for the exact new guest. The earlier protected retirement run
[`42295531572`](https://github.com/antiartificial/norn/actions/runs/42295531572)
ran before this create attempt and retired a prior attempt, so it does not
resolve this run's outcome. The workflow log identifies the Packer build
failure but the retained excerpt does not establish a more specific cause.
No successful guest creation was recorded, but final absence for this exact
attempt remains unproven. This is separate from the live Mini upgrade and
supports keeping Tart OS restoration off the critical path unless OS-image
installation itself is being qualified.
