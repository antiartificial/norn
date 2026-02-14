# Secrets

Norn v2 uses SOPS + age for secret management. Secrets are encrypted at rest and decrypted at deploy time, then injected as environment variables into Nomad tasks.

## How It Works

```mermaid
graph LR
    A[secrets.enc.yaml] -->|sops decrypt| B[Plaintext secrets]
    B -->|Pipeline submit step| C[Nomad task env vars]
```

1. Secrets are stored in a `secrets.enc.yaml` file alongside the app's `infraspec.yaml`
2. During the **submit** step of the deploy pipeline, Norn decrypts the file using SOPS
3. Decrypted key-value pairs are merged into the environment variables passed to `nomad.Translate()`
4. Nomad injects them as environment variables in the running containers

### v1 vs v2

In v1, secrets were synced to Kubernetes Secrets and mounted into pods. In v2, there is no Kubernetes — secrets go directly from SOPS to Nomad environment variables during the pipeline.

## Setup

### 1. Generate an age key

```bash
age-keygen -o ~/.config/sops/age/keys.txt
```

::: tip macOS
SOPS on macOS looks for age keys at `~/Library/Application Support/sops/age/keys.txt`. Symlink to avoid duplicating:

```bash
ln -sf ~/.config/sops/age/keys.txt \
  ~/Library/Application\ Support/sops/age/keys.txt
```
:::

### 2. Create a `.sops.yaml` in the app directory

```yaml
creation_rules:
  - age: >-
      age1xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

### 3. Create and encrypt secrets

```bash
# Create a plaintext file
cat > secrets.yaml <<EOF
DATABASE_URL: postgres://user:pass@host:5432/db
API_KEY: sk-1234567890
EOF

# Encrypt it
sops -e secrets.yaml > secrets.enc.yaml
rm secrets.yaml
```

### 4. Declare expected keys in infraspec

```yaml
secrets:
  - DATABASE_URL
  - API_KEY
```

The `secrets` list is informational — it tells the UI and CLI which keys to expect. The actual values come from `secrets.enc.yaml`.

## CLI Management

```bash
# List secret keys for an app
norn secrets myapp

# Set or update a secret
norn secrets set myapp DATABASE_URL=postgres://...

# Delete a secret
norn secrets delete myapp OLD_KEY
```

The `set` command decrypts the file, updates the key, and re-encrypts.

## Secrets Flow

```mermaid
sequenceDiagram
    participant CLI as CLI / UI
    participant API as Norn API
    participant SOPS as SOPS + age
    participant Nomad

    CLI->>API: POST /api/apps/{id}/deploy
    API->>SOPS: Decrypt secrets.enc.yaml
    SOPS-->>API: Plaintext key-value pairs
    API->>API: Merge spec.env + secrets
    API->>Nomad: Submit job with env vars
    Nomad->>Nomad: Inject env into containers
```
