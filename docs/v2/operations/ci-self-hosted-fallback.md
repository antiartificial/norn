# Ephemeral self-hosted CI fallback

Status: design only. Do not change the required workflows to use this lane
until the disposable runner provisioners and repository policy described below
exist and have been rehearsed. A persistent repository runner is not an
acceptable substitute.

## Current protected checks

The protected `master` branch requires these GitHub Actions check runs:

| Check run | Current image | Required fallback host |
| --- | --- | --- |
| `API Go tests` | `macos-26` | macOS 26, arm64 |
| `CLI Go tests` | `macos-26` | macOS 26, arm64 |
| `Fleet pilot workload` | `ubuntu-24.04` | Ubuntu 24.04, x86-64, Docker |
| `OpenAPI contract` | `ubuntu-24.04` | Ubuntu 24.04, x86-64 |
| `Web UI tests and build` | `ubuntu-24.04` | Ubuntu 24.04, x86-64 |
| `Workflow lint` | `ubuntu-24.04` | Ubuntu 24.04, x86-64 |
| `M4 durable replicas on three Nomad clients` | `ubuntu-24.04` | Ubuntu 24.04, x86-64, Docker and cgroups |

Keep the job `name` values unchanged. Branch protection binds the exact names
to the GitHub Actions app. A new workflow with differently named jobs, a local
test report, or a status posted by another app does not replace them.

The `macos-26` GitHub-hosted image is arm64. The two macOS jobs therefore need
a disposable macOS 26 arm64 host. The Mini is a persistent Norn runtime host
and must not be enrolled as a CI runner. It is also not a Linux substitute for
the five Ubuntu jobs.

## Why the workflow is unchanged

This repository is public, permits forks, and currently has no self-hosted
runners. Pull-request workflows execute code from the proposed commit. Sending
one of those jobs to a persistent self-hosted machine would give repository
code access to that machine and anything reachable from it.

A separate `workflow_dispatch` fallback also cannot satisfy a pull request's
required checks safely. GitHub associates that run and its check suite with the
dispatch ref's SHA, even if `actions/checkout` later checks out a supplied pull
request SHA. Reusing the seven names in that run would report on the wrong
commit.

Do not implement automatic timeout fallback between hosted and self-hosted
labels. GitHub queues a job until a matching runner appears and does not offer
a trusted `runs-on` fallback after a bounded wait. Duplicate jobs with the same
protected name make the required result ambiguous and weaken release admission
that expects one successful check from the GitHub Actions app.

## Required runner contract

Before enabling a self-hosted selector in the existing jobs, provide two
independent disposable pools:

- `norn-ci-macos-26-arm64`: a clean macOS 26 arm64 VM created for one job.
- `norn-ci-ubuntu-2404-x64`: a clean Ubuntu 24.04 x86-64 VM created for one job,
  with Docker and the cgroup behavior used by the Fleet and M4 scripts.

Each runner must use a just-in-time, one-job registration. Destroy the VM and
its disk after the job, whether the job succeeds, fails, is cancelled, or times
out. Registration tokens must remain in the external provisioner and must not
be repository secrets or files on a reusable host. The VM must start without
cloud, provider, Norn, release-signing, package-publishing, SSH, or tailnet
credentials. Allow outbound access only for GitHub Actions, pinned tool and
container downloads already used by the workflow, and language package
registries required by the checked-in lock files.

Apply explicit labels in addition to GitHub's default labels:

```text
self-hosted, macOS, ARM64, norn-ci-ephemeral, norn-ci-macos-26-arm64
self-hosted, Linux, X64, norn-ci-ephemeral, norn-ci-ubuntu-2404-x64
```

The custom labels are routing claims, not proof. The first step in every job
must run before checkout and fail unless `sw_vers -productVersion` begins with
`26.` and `uname -m` is `arm64`, or `/etc/os-release` identifies Ubuntu 24.04
and `uname -m` is `x86_64`. Linux jobs that require Docker must also prove the
daemon is reachable. The external provisioner must independently record the
one-job lifecycle and successful deletion; repository code cannot attest to
the destruction of the machine executing it.

## Event and repository admission

Keep the existing `pull_request` and protected `master` push triggers. Select a
self-hosted label only when all of these GitHub-supplied facts hold:

1. The repository is exactly `antiartificial/norn`.
2. The event is a push to protected `refs/heads/master`, or a pull request whose
   base is `master` and whose head repository is exactly
   `antiartificial/norn`.
3. A repository variable selects the reviewed ephemeral runner contract.
4. A matching clean runner was created for this exact job and is waiting.

Fork pull requests always remain on GitHub-hosted images. If same-repository
pull requests are admitted later, treat their source as untrusted despite the
repository match: the one-job VM, empty credentials, network boundary, and
destruction receipt are mandatory. Start with protected `master` pushes only;
expand to same-repository pull requests after adversarial qualification.

Do not use `pull_request_target`, because it would combine a privileged base
workflow context with pull-request-controlled code. Do not add a
`workflow_dispatch` release shortcut. Platform release signing remains in the
separate protected `platform-release` environment and continues to require the
exact protected-master qualification evidence. The fallback runner receives no
signing key and cannot publish or promote a release.

## Enablement sequence

1. Build and rehearse both external one-job provisioners with disposable test
   repositories. Prove cancellation and crash cleanup, not only success.
2. Record image provenance and verify the exact OS, architecture, Docker, disk,
   and cgroup contracts above.
3. Add a repository variable with a versioned value such as
   `NORN_CI_RUNNER_LANE=ephemeral-v1`. Absence or any other value must select
   the current GitHub-hosted image.
4. In the existing seven jobs only, change `runs-on` to choose the exact label
   array for protected `master` pushes. Add the pre-checkout host assertion.
   Preserve every job name, trigger, permission, command, pinned action, and
   concurrency rule.
5. Run a non-required rehearsal workflow first, then enable the selector for
   one protected `master` SHA. Confirm one check run per required name, the
   GitHub Actions app identity, exact head SHA, host assertions, and VM deletion
   receipts.
6. Disable the variable to return immediately to `macos-26` and
   `ubuntu-24.04`. Remove any queued self-hosted jobs before taking the pool
   offline.

Do not extend the lane to release, Fleet apply, recovery, artifact publication,
or environment-secret jobs. Those workflows have separate authority and
evidence contracts.
