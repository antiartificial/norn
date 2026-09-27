# Linux cgroup v2 snapshot integration harness

Run the real supervisor snapshot path in an isolated, privileged PostgreSQL 16 container:

```bash
cd /path/to/norn
v2/scripts/test-snapshot-cgroup-linux.sh
```

The wrapper starts `postgres:16` with a private cgroup namespace, mounts the checkout read-only, bootstraps a temporary Unix-socket PostgreSQL cluster, and builds the runner in the container. It runs only `TestLinuxCgroupSnapshotRunner`.

The test proves these runtime properties against real cgroup v2 files:

- `NewCgroupBackend`, `StartSnapshot`, and `ObserveSnapshot` use a real writable cgroup v2 hierarchy.
- a checksum-pinned PostgreSQL 16 `pg_dump` produces a recoverable artifact; success is accepted only after `cgroup.events` reports empty and `cgroup.procs` is empty.
- normal helper completion removes its private service and password files before publishing the terminal result.
- a separately checksum-pinned wrapper that pauses before `exec` of real `pg_dump` is killed through `cgroup.kill`; `ObserveSnapshot` returns unknown and removes the running helper's private service and password files.

The harness is Linux-only and opt-in because `UseCgroupFD`, `cgroup.kill`, and writable cgroup v2 delegation are unavailable on macOS. It needs Docker access, network access during the ephemeral container's Go toolchain download, and a Docker runtime that permits `--privileged --cgroupns=private`. It does not start Norn, connect to a Norn control plane, mutate a provider, or retain PostgreSQL state after the container exits.
