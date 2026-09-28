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

These local checks do not cover the workflow's schema-tool installation,
OpenTofu validation matrix, action lint, or deployment on protected Fleet
nodes. M4 remains open pending hosted or equivalently controlled validation,
protected-node publication, public traffic proof, scaling, and drain.
