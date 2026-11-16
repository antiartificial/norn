# Retire the frozen Kubernetes implementation

This source cleanup follows the `v3.0.0` tag. It removes the obsolete
implementation and its executable entrypoints together. It does not change a
running system, provider state, signed artifact, or the existing tag.

## Removed source

| Paths | Retired behavior |
| --- | --- |
| Root `api/` and `cli/` | Kubernetes API/runtime, cluster provisioning and kubeconfig commands, and their old Go modules |
| Root `ui/` | Frozen dashboard, dependencies/assets, and three UI test files |
| Root `infra/` | k3s/Lima and multi-provider Terraform provisioning, old Compose/ACL fixtures |
| Root `Dockerfile` and `infraspec.yaml` | Container and deployment definitions that still built the frozen API |
| Old root Makefile recipes | Obsolete worker, Kubernetes, and legacy infrastructure commands |

The root source trees contained 150 tracked files, including nine Go test files
and three UI test files. Removing the Dockerfile and InfraSpec brings the source
deletion set to 152 files. The root Makefile now explicitly delegates supported
runtime targets with `make -C v2`; it does not include a Makefile whose relative
paths would be interpreted at the wrong root.

Reproduce the pre-cleanup inventory with:

```sh
git ls-tree -r --name-only v3.0.0 api cli ui infra
```

## Preserved boundaries

All V1/V2 documentation remains in place. The old source is available by
checking out `v3.0.0`; Git history is not rewritten. Active runtime code under
`v2/`, versioned API/schema fixtures, release compatibility metadata, and
historical-baseline recovery scripts remain unchanged.

Current CI and platform publishing build the active `v2/` modules. Relative
references such as `../../infra` inside `v2/api/nomad` resolve into `v2/infra`,
not the retired root. A reusable application workflow's Dockerfile input names
the caller's build context, not the retired platform Dockerfile.

### The root module still affects a pinned verifier

`v2/scripts/build-m5-intent-verifier.sh` builds a file-list Go program from the
repository root. The root Go 1.24.2 directive affects `DefaultGODEBUG` values
even when building with the pinned Go 1.26.10 toolchain.

A controlled build with the same source and flags matches the expected
`2bc04575ed02e5240780cbe1f2e09e7cd695cf2197a1cbc6ba3d0c4594c46b8c`
SHA-256 with the root module, but produces different bytes without it.
Compilation success alone would miss this break. Keep root `go.mod` until a
separate explicit verifier build/pin migration is qualified. Normal PR workflow
validation now invokes this existing hash guard, so a future module cleanup
cannot silently change that artifact.

## Verification boundary

The deletion is accepted only with actual active binary builds, the pinned
verifier hash check, active API/CLI/UI validation, documentation link/build
checks, and protected repository CI. A source search alone does not prove
packaging works. Optional integrations need their own runtime fixtures; this
cleanup does not renew their qualification or publish a new release.

The [repository split plan](architecture/repository-boundaries.md) assigns the
remaining source, tests, contracts, and release trust to future workstreams.

## Local evidence

After deleting the frozen trees, the active API/CLI/host and ingress tools build
from their real modules. The pinned-Go build uses `-buildvcs=false`, matching
release packaging and avoiding the local Go 1.26 linked-worktree VCS lookup.
The verifier rebuild matches its retained hash. API contract/model/Nomad tests
pass, the active UI builds and passes 161 tests, and the complete docs build
passes with dead-link checking enabled.

These local checks do not replace protected CI or live integration fixtures.
The active `v2/` source and release catalog are byte-for-byte unchanged by this
retirement; only their supported root entrypoints and validation wiring change.
