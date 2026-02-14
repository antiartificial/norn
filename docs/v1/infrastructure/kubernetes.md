# Kubernetes

Norn uses the Kubernetes API (via `client-go`) to manage deployments, services, pods, and secrets. Kubernetes is optional for local development — the API gracefully degrades when no cluster is available.

## Graceful degradation

When no Kubernetes cluster is connected:

- The API starts normally and serves app discovery, health checks, and dashboard data
- K8s-dependent actions return a clear **503 Service Unavailable** error:
  - Deploy: `"deploy requires Kubernetes"`
  - Restart: `"restart requires Kubernetes"`
  - Rollback: `"rollback requires Kubernetes"`
  - Log streaming: `"log streaming requires Kubernetes"`
  - Forge: `"forge requires Kubernetes"`
  - Teardown: `"teardown requires Kubernetes"`

This means you can develop the UI and CLI locally without a cluster running.

## Namespace model

All app resources are created in the `default` namespace. The Cloudflare tunnel components live in a dedicated `cloudflared` namespace.

## Resources managed

### Deployments

Created by the forge pipeline with:

- Name matching the app ID
- Replicas from infraspec (default 1)
- Container port from infraspec
- Liveness probe using the healthcheck path
- Secret references (if the app has secrets)
- Environment variables from `env` field
- Auto-generated `DATABASE_URL` for apps with postgres dependency
- Volume mounts (PVC or hostPath)

Updated by the deploy pipeline — the image tag is changed to the newly built image.

### Services

Created by the forge pipeline:

- ClusterIP service mapping the internal hostname to the app's port
- Selector matches the deployment's app label

### Secrets

Created by the SOPS manager during deploy:

- Named `<app>-secrets`
- Contains decrypted secret values from `secrets.enc.yaml`
- Applied via `kubectl apply -f -` (piped via stdin for security)

### PersistentVolumeClaims

Created by the forge pipeline for apps with volume specs:

- Named `<app>-<volume-name>`
- Size from the infraspec volume definition
- Labels: `managed-by: norn`, `app: <app>`

## K8s client operations

The `k8s.Client` wrapper provides:

| Operation | Description |
|-----------|-------------|
| `GetPods` | List pods for an app by label selector |
| `StreamLogs` | Stream pod logs (with optional follow) |
| `SetImage` | Update deployment container image |
| `RestartDeployment` | Trigger rolling restart (annotation patch) |
| `CreateDeployment` | Create a new Deployment resource |
| `DeleteDeployment` | Delete a Deployment |
| `CreateService` | Create a new Service |
| `DeleteService` | Delete a Service |
| `CreatePVC` | Create a PersistentVolumeClaim |
| `PatchConfigMap` | Read-modify-write a ConfigMap (for cloudflared config) |

## Error handling

The client uses helpers for common K8s error conditions:

- `k8s.IsAlreadyExists(err)` — resource already exists (idempotent operations)
- `k8s.IsNotFound(err)` — resource doesn't exist (graceful skips in teardown)
