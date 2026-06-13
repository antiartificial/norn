# Object Storage

Norn v2 supports S3-compatible object storage for app buckets. The default local provider is Garage: one platform-scoped Garage service can host many buckets, while apps declare only the buckets they need.

## Platform Model

Garage should run as local platform infrastructure, not as one container per app. Norn treats it like a managed backing service:

- Garage daemon and data volumes are platform-scoped.
- App deploys never tear down Garage.
- Apps declare buckets in `infraspec.yaml`.
- Norn provisions buckets and app-scoped credentials during deploy.
- Runtime tasks receive ordinary S3-compatible environment variables.

This keeps local infra small while still making bucket ownership declarative and visible.

## Garage Service

Run Garage with durable host volumes for metadata and data. The S3 endpoint can stay local or tailnet-only, and the admin endpoint should stay private.

Norn's checked-in dev Nomad configs reserve these host volumes:

```hcl
host_volume "garage-meta" {
  path      = "/Users/0xadb/volumes/garage-meta"
  read_only = false
}

host_volume "garage-data" {
  path      = "/Users/0xadb/volumes/garage-data"
  read_only = false
}
```

Suggested ports:

| Port | Purpose |
|------|---------|
| `3900` | S3 API |
| `3901` | RPC between Garage nodes |
| `3902` | Web endpoint, if enabled |
| `3903` | Admin API |

Norn needs S3 credentials for health checks and fallback bucket creation. For managed Garage keys and per-app permissions, also configure the Garage admin API:

```bash
export NORN_S3_PROVIDER=garage
export NORN_S3_ENDPOINT=http://127.0.0.1:3900
export NORN_S3_ACCESS_KEY=<garage-root-or-operator-key>
export NORN_S3_SECRET_KEY=<garage-root-or-operator-secret>
export NORN_S3_REGION=garage
export NORN_S3_USE_SSL=false
export NORN_S3_FORCE_PATH_STYLE=true

export NORN_GARAGE_ADMIN_ENDPOINT=http://127.0.0.1:3903
export NORN_GARAGE_ADMIN_TOKEN=<garage-admin-token>
```

Garage clients should use path-style addressing. Norn injects both `S3_FORCE_PATH_STYLE=true` and `AWS_S3_FORCE_PATH_STYLE=true` for apps that declare Garage storage.

## App Infraspec

Declare one or more buckets under `infrastructure.objectStorage`:

```yaml
infrastructure:
  objectStorage:
    provider: garage
    buckets:
      - name: omniphore-media
        access: readWrite
        env: MEDIA
        prefix: prod/
      - name: omniphore-snapshots
        access: readWrite
        env: SNAPSHOTS
```

`access` can be:

| Value | Granted permissions |
|-------|---------------------|
| `readOnly` | Read |
| `readWrite` | Read and write |
| `owner` | Read, write, and owner |

`public` is accepted in the schema for future exposure policy, but Norn does not yet publish public bucket websites or public object URLs.

## Runtime Environment

During deploy, Norn injects shared S3 settings:

| Variable | Description |
|----------|-------------|
| `S3_ENDPOINT` | Endpoint URL, including scheme |
| `S3_REGION` | Region value for S3 clients |
| `S3_PROVIDER` | Provider hint, such as `garage` |
| `S3_BUCKETS` | Comma-separated bucket names |
| `AWS_ACCESS_KEY_ID` | App-scoped access key when Garage admin is configured |
| `AWS_SECRET_ACCESS_KEY` | App-scoped secret key when Garage admin is configured |
| `S3_FORCE_PATH_STYLE` | `true` for Garage |
| `AWS_S3_FORCE_PATH_STYLE` | `true` for Garage-compatible SDKs |

The first declared bucket is also exposed as:

| Variable | Description |
|----------|-------------|
| `S3_BUCKET` | First bucket name |
| `S3_PREFIX` | First bucket prefix, if set |

Every bucket also receives an alias-specific variable. With `env: MEDIA`, Norn injects:

```text
S3_BUCKET_MEDIA=omniphore-media
S3_PREFIX_MEDIA=prod/
```

If `env` is omitted, Norn derives an uppercase alias from the bucket name.

## Credentials

When `NORN_GARAGE_ADMIN_ENDPOINT` and `NORN_GARAGE_ADMIN_TOKEN` are configured, Norn creates or reuses a Garage key named `norn-<app>` and grants it access to every declared bucket. Generated credentials are injected into the deploy environment and Norn attempts to persist them into the app's `secrets.enc.yaml` as:

```text
AWS_ACCESS_KEY_ID
AWS_SECRET_ACCESS_KEY
```

If the Garage admin API is not configured, Norn falls back to the configured `NORN_S3_ACCESS_KEY` and `NORN_S3_SECRET_KEY`. That mode is useful for pre-provisioned buckets, but managed Garage is preferred for local Norn-hosted infrastructure.

## Health

`norn health` reports the S3-compatible endpoint as `s3` when `NORN_S3_ENDPOINT` is configured. The health check uses the configured S3 credentials and does not expose secret values.
