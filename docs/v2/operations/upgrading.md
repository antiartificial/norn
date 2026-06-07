# Upgrading Norn

Use this runbook when Norn itself is installed as the local LaunchAgent `com.norn.api`.

The safe upgrade path restarts only the Norn API process. Do not use `make down` for a production-ish local upgrade because it also stops Nomad and Consul, which can disrupt hosted apps.

## Safe Local Upgrade

```bash
cd /Users/0xadb/projects/norn

stamp=$(date +%Y%m%dT%H%M%S)
mkdir -p /Users/0xadb/go/bin/norn-backups/$stamp
cp -p /Users/0xadb/go/bin/norn-api /Users/0xadb/go/bin/norn-backups/$stamp/
cp -p /Users/0xadb/go/bin/norn /Users/0xadb/go/bin/norn-backups/$stamp/

cd v2/ui && pnpm build
cd /Users/0xadb/projects/norn/v2 && make build

install -m 0755 bin/norn-api /Users/0xadb/go/bin/norn-api
install -m 0755 bin/norn /Users/0xadb/go/bin/norn

launchctl kickstart -k gui/$(id -u)/com.norn.api
```

## Smoke Checks

```bash
norn version
norn ops platform
norn services
norn status
curl -sf http://127.0.0.1:8800/api/health
```

Open `http://127.0.0.1:8800` and check the Platform tab. The Platform tab should show service counts, deployment provenance, snapshot retention state, access counts, and observability status.

## Rollback

Use the backup path printed or recorded during the upgrade:

```bash
backup=/Users/0xadb/go/bin/norn-backups/YYYYMMDDTHHMMSS

install -m 0755 "$backup/norn-api" /Users/0xadb/go/bin/norn-api
install -m 0755 "$backup/norn" /Users/0xadb/go/bin/norn
launchctl kickstart -k gui/$(id -u)/com.norn.api
```

Then rerun the smoke checks.

## Notes

- The root `Makefile` still targets the older non-v2 tree. Use `v2/Makefile` for v2 releases.
- `NORN_UI_DIR` should point at `/Users/0xadb/projects/norn/v2/ui/dist` when the API serves the built dashboard.
- Keep Nomad, Consul, Postgres, and app allocations running during a Norn API upgrade unless you are intentionally rebuilding the whole dev environment.
