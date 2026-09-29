# Mini host-volume ownership join — 2026-09-26

Read-only Mini Nomad job and node inspection at approximately 23:15 UTC.
The 37 base jobs declared nine group-volume uses across eight jobs, referring
to five distinct Nomad host-volume sources. Periodic children were excluded.
The node catalog had one Nomad node. No source paths, job environment values,
file contents, or credentials were copied into this repository. No Nomad job,
mount, file, or backup was changed.

| Host-volume source | Current consumers | Mount intent | Disk allocated at read |
| --- | --- | --- | ---: |
| `ft-bookmarks` | `field-harbor` web, AM sync, PM sync, media-review-storage | Four read/write uses of one source | 10.62 GiB |
| `docker-socket` | `norn-cadvisor` web and docker-stats groups | Two read-only declarations; an API socket, not a backup data directory | — |
| `norn-prometheus-data` | `norn-prometheus` web | Read/write | 2.50 GiB |
| `signal-cli-data` | `signal-cli` web | Read/write | 3.87 GiB |
| `signal-sideband-media` | `signal-sideband` web | Read/write | 2.80 GiB |

The four writable sources existed as owner-local directories. `du -sk`
reported 19.79 GiB in aggregate across those four samples. All four resolved
to the same host filesystem, which had 35.81 GiB available at the observation
time. These are allocated-byte samples, not a growth rate, consistent snapshot,
backup manifest, or restore proof. Nomad host-volume configuration establishes
node-local storage, not off-host durability. A read-only socket mount also
does not imply that Docker API access is harmless; its security policy is a
separate review.

## M0/M5 disposition

- Keep the four writable volume mappings, their consuming job identities, and
  mount modes in the Mini preflight and postflight manifest. The v2→v3 control
  upgrade should leave those jobs and directories in place; a matching Nomad
  definition alone does not prove application data was preserved.
- Assign an application owner to classify each directory as authoritative
  state, reconstructable cache, or bounded diagnostics. Record its independent
  backup/restore path and acceptable loss before counting that workload as
  protected by the Mini release. Do not infer a volume backup from the control
  PostgreSQL dump.
- Recheck the same node path identity and a private application-level sentinel
  across the scheduled upgrade rehearsal. Do not publish raw source paths,
  private file names, or content in the CI fixture. The current read-only join
  does not authorize moving, pruning, or snapshotting these directories.

The app database targets and unmanaged host data remain separate inventory
items. This join narrows the declared Nomad volume boundary; it does not close
M0 or M5.
