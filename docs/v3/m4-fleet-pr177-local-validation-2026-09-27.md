# Fleet ingress PR #177 local validation — 2026-09-27

Fleet draft [PR #177](https://github.com/antiartificial/norn-fleet/pull/177)
was checked at head `daee1a1f3efc8595fd56e8e8b041d4c7e55a4cc9`.
The checkout was clean before and after validation.

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts -p 'test_*.py'
ansible-playbook -i ansible/inventory.example.yml ansible/site.yml --syntax-check
```

The Python suite exited zero: **1,413 tests in 242.223 seconds, 3 skipped**.
The Ansible syntax check exited zero. Its output included an existing
`apt_repository` deprecation warning and unmatched role patterns from the
example inventory. The focused ingress-related `test_fleet_hooks.py` and
`test_reconcile.py` groups also passed separately, 23 and 22 tests.

The [GitHub contract job](https://github.com/antiartificial/norn-fleet/actions/runs/36359218933/job/108732903868)
failed before any runner step. Its check annotation says GitHub Actions did
not start the job because of account billing or a spending limit. The
repository variable for the documented temporary Linux X64 self-hosted CI
fallback is absent, and no runner with that fallback label is registered.
No hosted contract receipt exists for this head.

Additional local checks passed against the same Fleet head:

- `scripts/sanity.py` and `check-jsonschema==0.33.3` with `PyYAML==6.0.2`
  accepted all four cluster documents. Production emitted the existing
  repository-placeholder warning.
- `actionlint` v1.7.7 accepted the GitHub workflow files, and
  `tofu fmt -check -recursive` passed.
- The workflow's four ordinary OpenTofu roots passed `init -backend=false
  -lockfile=readonly -input=false` and `validate` with the pinned OpenTofu
  1.10.6 binary. Both disposable roots passed the same commands with the
  pinned OpenTofu 1.12.6 binary. The 1.10.6 archive checksum matched its
  published SHA256SUMS file. The external-Mac disposable root warned that
  provider selections differed from its read-only lockfile, but validation
  succeeded.

These checks ran on local Darwin arm64, while the hosted workflow uses Linux.
The local Ansible syntax check used core 2.21.3 rather than the workflow's
pinned 2.18.3. There is still no hosted contract receipt or protected-node
deployment. M4 remains open pending protected-node publication, public traffic
proof, scaling, and drain.
