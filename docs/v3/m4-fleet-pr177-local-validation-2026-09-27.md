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

## Publisher bootstrap follow-up — 2026-09-28

Fleet head `691c82b5c0a2ab2adc5658f9d21d075de3b00ec3` now requires the
controller to prove the private publisher's mTLS `/v1/health` response and
exact inventory node ID during enabled bootstrap. The controller's client
certificate, key, and publisher server CA must be owner-only files. This is a
bootstrap identity check, not an app-route publication or traffic proof.

At this exact head, `ansible-playbook -i ansible/inventory.example.yml
ansible/site.yml --syntax-check` and 45 focused Fleet hook/reconciliation
Python tests passed locally. The hosted `contract` check on [run
36380615809](https://github.com/antiartificial/norn-fleet/actions/runs/36380615809)
failed with zero job steps, so it supplies no hosted test evidence. The full
1,413-test local receipt above initially applied to its earlier pinned head.

An exact-head refresh at `691c82b5c0a2ab2adc5658f9d21d075de3b00ec3`
ran `PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts
-p 'test_*.py'` on 2026-09-28. It exited zero: **1,413 tests in 251.986
seconds, 3 skipped**. The Fleet checkout was clean afterward. This refresh
covers the full local Python contract suite at the publisher identity-check
commit; the same-head Ansible syntax check above covers its playbook change.
It does not replace the blocked hosted Linux contract job or protected-node
publication evidence.

A second exact-head run of the same full Python discovery command on
2026-09-28 exited zero: **1,413 tests in 243.296 seconds, 2 skipped**. The
different skip count is recorded as observed; neither run supplies the hosted
Linux contract or protected ingress proof.
