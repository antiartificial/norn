# M2 generated-job database delivery on pinned Nomad

Date: 2026-09-24. Scope: disposable local macOS Nomad agent and Docker daemon.

The opt-in `TestGeneratedWordPressDatabaseDeliveryInNomad` ran against Nomad
1.9.7, the HA lab's pinned package version (`v2/infra/ha-lab/ansible/group_vars/all.yml`).
The downloaded Darwin arm64 ZIP matched HashiCorp's published SHA-256 sum
`90f87dffb3669a842a8428899088f3a0ec5a0d204e5278dbb0c1ac16ab295935`.
Docker was 29.8.0 and the locally present `wordpress:latest` image ID was
`sha256:8746e7e072e3e9e12144fb070928f81190639ef38e4d00cb959463d9ce2ab1d6`.

The test generated Norn service, periodic, and function jobs using catalog
revision 7. It staged WordPress component values in each job's Nomad Variable,
copied the revision for the function, registered the jobs, forced the periodic
run, and read allocation stdout. Each allocation printed the SHA-256 digest of
its `WORDPRESS_DB_PASSWORD` environment value. All three digests matched the
test's synthetic value, including spaces, quotes, a backslash, and a hash.

Command and result:

```text
NORN_TEST_NOMAD_ADDR=http://127.0.0.1:14646 go test ./nomad -run TestGeneratedWordPressDatabaseDeliveryInNomad -count=1 -v
--- PASS: TestGeneratedWordPressDatabaseDeliveryInNomad (16.17s)
PASS
```

This proves the generated-job delivery path and exact component bytes on the
pinned Nomad version. It does not prove a MySQL connection, WordPress startup,
TLS usage, credential rotation, archive retention, production topology, or
application rollback. The test is opt-in because it requires a disposable
Nomad agent with the Docker driver and the specified local image. All jobs
and variables use unique IDs and are deregistered by test cleanup.
