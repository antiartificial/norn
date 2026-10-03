# Mini live readback — 2026-10-02

At `2026-10-02T22:45:14Z`, a read-only SSH session as the Mini operator used
the Norn CLI and loopback API to observe the existing Mini runtime. The
owner-only local source record is
`/Users/arti/Desktop/Claude/.norn-pilot-evidence/v3-mini-baseline-2026-10-02.json`.
That private file is not part of this repository or available to remote CI;
this page retains only its non-secret findings and evidence limits.

| Observation | Result |
| --- | --- |
| Live release SHA | `a5da8ef15d12e9eca7561e90b90d96f6dc652a21` |
| Reported version | `v2.20.0-platform-30-ga5da8ef` |
| API health / host doctor / smoke | `ok` / `pass` / `pass` |
| Services / active operations | 46 / 0 |
| Recent deploys | 20 succeeded, 0 failed, 0 dirty |
| Substrate | Consul, Nomad, PostgreSQL, S3, and SOPS reported up |
| Production readiness | blocked: 7 passed, 18 failed, 1 warning |

The readback did not rehash the installed binary or manifest. It is not a
Mini v3 promotion, rollback rehearsal, or Fleet runtime observation. Snapshot
retention and secret-attention warnings remain for release qualification.
