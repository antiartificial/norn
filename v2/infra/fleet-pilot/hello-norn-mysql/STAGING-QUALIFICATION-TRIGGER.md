# Staging image qualification trigger

This path-local receipt intentionally triggers the protected-master staging
release caller for `hello-norn-mysql`. It changes neither the workload source,
its Nomad jobs, nor its runtime contract.

- Fleet integration merged SHA: `6ff2397008accf3f694ab4cbcc4d97ed7df31361`
- Norn platform release SHA: `a04825b866686871a49b7ee325892031360870cd`
- Release caller: `.github/workflows/hello-norn-mysql-release.yml`

After this document is merged through protected `master`, the caller builds a
new immutable pilot image from that exact source commit and asks Norn to stage
and qualify it. A qualification is produced only by the protected workflow and
the Norn control plane; this document is a trigger record, not a qualification
or promotion receipt.
