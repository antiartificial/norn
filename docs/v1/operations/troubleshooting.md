# Troubleshooting

Common issues you may encounter when running Norn, with diagnosis steps and fixes.

## 1. Kubernetes Not Connected

**Symptoms:** API returns 503 errors for deploy, restart, rollback, or logs operations.

**Diagnosis:**
```bash
kubectl cluster-info
```

If this fails or shows no cluster, Kubernetes is not properly configured.

**Fix:**
- For local development: Start minikube with `minikube start`
- For remote clusters: Configure your kubeconfig file to point to the cluster
- Verify connectivity: `kubectl get nodes`

**Note:** The API still works for discovery and dashboard views without Kubernetes. Only orchestration operations require an active cluster connection.

## 2. Database Not Accessible

**Symptoms:** Norn API fails to start with database connection errors. Operations that write to the database fail.

**Diagnosis:**
```bash
pg_isready
psql -U norn -d norn_db -c "SELECT 1"
```

If either command fails, PostgreSQL is not running or the database doesn't exist.

**Fix:**
```bash
make db
```

This creates the `norn` user and `norn_db` database. The command is idempotent and safe to run multiple times.

## 3. SOPS Age Key Missing

**Symptoms:** Secret operations fail with encryption/decryption errors.

**Diagnosis:**
```bash
test -f ~/.config/sops/age/keys.txt && echo "Key exists" || echo "Key missing"
```

**Fix:**

Generate a new age key:
```bash
age-keygen -o ~/.config/sops/age/keys.txt
```

**macOS users:** SOPS looks for keys at `~/Library/Application Support/sops/age/keys.txt`. Create a symlink:
```bash
mkdir -p ~/Library/Application\ Support/sops/age
ln -s ~/.config/sops/age/keys.txt ~/Library/Application\ Support/sops/age/keys.txt
```

## 4. Deploy Stuck in Building/Deploying

**Symptoms:** A deployment shows status `building` or `deploying` indefinitely. The UI or CLI shows the app as stuck.

**Cause:** The API crashed during a deploy, leaving the status in a non-terminal state.

**Fix:**

Restart the API:
```bash
make api
```

On startup, Norn automatically detects in-flight deployments and marks them as failed, allowing you to retry.

## 5. Port 8800 Already in Use

**Symptoms:** API fails to start with "address already in use" error on port 8800.

**Diagnosis:**
```bash
lsof -i :8800
```

This shows which process is using the port.

**Fix:**

Kill the conflicting process or stop the other Norn API instance.

## 6. Port 5173 Already in Use

**Symptoms:** UI dev server fails to start with "address already in use" error on port 5173.

**Diagnosis:**
```bash
lsof -i :5173
```

**Fix:**

Kill the conflicting Vite process. Multiple `make ui` or `make dev` commands can spawn duplicate servers.

## 7. Docker Build Fails

**Symptoms:** Deploy pipeline fails at the "Build" step with Docker errors.

**Diagnosis:**
- Check that Docker daemon is running: `docker ps`
- Verify the Dockerfile path in your infraspec matches the actual file location
- Check the build context directory is correct

**Fix:**
- Start Docker Desktop or the Docker daemon
- Correct the Dockerfile path in `infraspec.yaml`
- Ensure all files referenced in the Dockerfile exist

## 8. Forge Fails Mid-Way

**Symptoms:** Forge operation stops partway through with an error.

**Good news:** Forge is resumable. Simply run the command again:
```bash
norn forge myapp
```

Forge picks up from the last completed step and continues from there.

## 9. Valkey/Redpanda Not Running

**Symptoms:** API logs show connection errors for Valkey (Redis) or Redpanda (Kafka).

**Fix:**
```bash
make infra
```

This starts both services via Docker Compose. To stop them:
```bash
make infra-stop
```

## Interactive Health Checker

Toggle services to see error messages and fix instructions:

<HealthChecker />
