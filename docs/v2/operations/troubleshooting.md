# Troubleshooting

## Health Check

Run the built-in health check to verify all backing services:

```bash
norn health
# alias:
norn doctor
```

This checks connectivity to:
- Nomad API
- Consul API
- PostgreSQL database
- S3 storage (if configured)

## Common Issues

### Nomad Not Running

**Symptom**: `WARNING: nomad unavailable` on API startup, deploys fail.

**Fix**: Start Nomad:
```bash
# Dev mode
nomad agent -dev -bind=0.0.0.0

# Production
sudo systemctl start nomad
```

Verify: `curl http://localhost:4646/v1/status/leader`

### Consul Not Running

**Symptom**: `WARNING: consul not healthy`, services not discoverable.

**Fix**: Start Consul:
```bash
# Dev mode
consul agent -dev

# Production
sudo systemctl start consul
```

Verify: `curl http://localhost:8500/v1/status/leader`

### Database Connection Failed

**Symptom**: `database: dial tcp ... connection refused`

**Fix**: Ensure PostgreSQL is running and the connection string is correct:
```bash
# Check if PostgreSQL is running
pg_isready

# Verify connection
psql "postgres://norn:norn@localhost:5432/norn_v2?sslmode=disable"
```

### SOPS Key Missing

**Symptom**: Deploys fail at the submit step with SOPS decryption errors.

**Fix**: Ensure the age key exists:
```bash
ls ~/.config/sops/age/keys.txt
```

On macOS, also check the Application Support path:
```bash
ls ~/Library/Application\ Support/sops/age/keys.txt
```

If missing, symlink:
```bash
mkdir -p ~/Library/Application\ Support/sops/age/
ln -sf ~/.config/sops/age/keys.txt \
  ~/Library/Application\ Support/sops/age/keys.txt
```

### Host Volume Not Found

**Symptom**: Nomad allocation fails with "volume not found" error.

**Fix**: Add the `host_volume` stanza to the Nomad client config and restart:
```hcl
client {
  host_volume "volume-name" {
    path      = "/opt/volumes/volume-name"
    read_only = false
  }
}
```

See [Volumes](/v2/infrastructure/volumes) for full setup.

### Deploy Stuck at Healthy Step

**Symptom**: Deploy hangs after submit, never reaches healthy.

**Possible causes**:
1. Container is crashing — check Nomad allocation logs: `nomad alloc logs <alloc-id>`
2. Health check failing — verify the health check path returns 200
3. Port conflict — another allocation is using the static port

### InfraSpec Validation Errors

Run the validator to check your infraspec:
```bash
norn validate myapp
```

Common issues:
- Missing `name` field
- Process with `port` but no `health` check
- Invalid cron expression in `schedule`
- Volume name doesn't match Nomad host_volume

## Debugging Deploys

### View Saga Events

Every deploy creates a saga with detailed step-by-step events:

```bash
# Get the saga ID from the deploy output, then:
norn saga <saga-id>

# Or view recent events for an app:
norn saga --app=myapp --limit=50
```

### Check Nomad UI

Open `http://localhost:4646` to see:
- Job status and allocation health
- Task logs
- Resource utilization
- Failed allocation events

### Check API Logs

The Norn API logs all requests and pipeline events to stdout. Look for `pipeline:` prefixed messages for deploy issues.
