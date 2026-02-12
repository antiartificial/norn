# Object Storage

Norn supports S3-compatible object storage as a shared service. Locally, it runs MinIO; in production, you can use Cloudflare R2, AWS S3, Google Cloud Storage, or DigitalOcean Spaces.

## infraspec

```yaml
services:
  storage:
    bucket: my-app-uploads
    provider: minio  # or r2, s3, gcs, spaces
```

### Fields

| Field | Type | Description |
|-------|------|-------------|
| `services.storage.bucket` | string | Bucket name to create/use |
| `services.storage.provider` | string | Storage provider hint (for documentation; all use S3 protocol) |

## How it works

1. **Forge** creates the bucket in the configured S3 endpoint (if it doesn't exist)
2. **Deploy** injects storage credentials as environment variables into the K8s Deployment:

| Variable | Description |
|----------|-------------|
| `S3_ENDPOINT` | S3-compatible endpoint URL |
| `S3_BUCKET` | Bucket name |
| `AWS_ACCESS_KEY_ID` | Access key |
| `AWS_SECRET_ACCESS_KEY` | Secret key |

## Local development (MinIO)

MinIO runs as part of Norn's shared infrastructure:

```bash
make infra   # starts Valkey, Redpanda, and MinIO
```

- **API**: `localhost:9000`
- **Console**: `localhost:9001`
- **Credentials**: `norn` / `nornnorn`

The MinIO console at [localhost:9001](http://localhost:9001) provides a web UI for browsing buckets and objects.

### Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `NORN_S3_ENDPOINT` | `localhost:9000` | S3 endpoint |
| `NORN_S3_ACCESS_KEY` | `norn` | Access key |
| `NORN_S3_SECRET_KEY` | `nornnorn` | Secret key |
| `NORN_S3_REGION` | `us-east-1` | Region |
| `NORN_S3_USE_SSL` | `false` | Use HTTPS |

## Production providers

All providers use the same S3-compatible protocol. The `provider` field in the infraspec is informational — Norn connects to whatever endpoint is configured.

### Cloudflare R2

```bash
export NORN_S3_ENDPOINT=<account-id>.r2.cloudflarestorage.com
export NORN_S3_ACCESS_KEY=<r2-access-key>
export NORN_S3_SECRET_KEY=<r2-secret-key>
export NORN_S3_USE_SSL=true
```

R2 has no egress fees, making it ideal for serving user-uploaded content.

### AWS S3

```bash
export NORN_S3_ENDPOINT=s3.amazonaws.com
export NORN_S3_ACCESS_KEY=<aws-access-key>
export NORN_S3_SECRET_KEY=<aws-secret-key>
export NORN_S3_REGION=us-east-1
export NORN_S3_USE_SSL=true
```

### Google Cloud Storage

```bash
export NORN_S3_ENDPOINT=storage.googleapis.com
export NORN_S3_ACCESS_KEY=<gcs-hmac-key>
export NORN_S3_SECRET_KEY=<gcs-hmac-secret>
export NORN_S3_USE_SSL=true
```

GCS supports S3-compatible access via HMAC keys.

### DigitalOcean Spaces

```bash
export NORN_S3_ENDPOINT=<region>.digitaloceanspaces.com
export NORN_S3_ACCESS_KEY=<spaces-key>
export NORN_S3_SECRET_KEY=<spaces-secret>
export NORN_S3_REGION=<region>
export NORN_S3_USE_SSL=true
```

## SDK examples

### Go (minio-go)

```go
import "github.com/minio/minio-go/v7"

client, _ := minio.New(os.Getenv("S3_ENDPOINT"), &minio.Options{
    Creds:  credentials.NewStaticV4(
        os.Getenv("AWS_ACCESS_KEY_ID"),
        os.Getenv("AWS_SECRET_ACCESS_KEY"),
        "",
    ),
})

// Upload
client.PutObject(ctx, os.Getenv("S3_BUCKET"), "photo.jpg", reader, size, minio.PutObjectOptions{})

// Download
obj, _ := client.GetObject(ctx, os.Getenv("S3_BUCKET"), "photo.jpg", minio.GetObjectOptions{})
```

### Node.js (aws-sdk)

```javascript
const { S3Client, PutObjectCommand } = require('@aws-sdk/client-s3')

const s3 = new S3Client({
  endpoint: `http://${process.env.S3_ENDPOINT}`,
  credentials: {
    accessKeyId: process.env.AWS_ACCESS_KEY_ID,
    secretAccessKey: process.env.AWS_SECRET_ACCESS_KEY,
  },
  forcePathStyle: true,
})

await s3.send(new PutObjectCommand({
  Bucket: process.env.S3_BUCKET,
  Key: 'photo.jpg',
  Body: buffer,
}))
```

## Health check

Norn's `/api/health` endpoint includes S3/MinIO connectivity. Check with:

```bash
norn health
# or
curl http://localhost:8800/api/health | jq '.services[] | select(.name == "s3/minio")'
```
