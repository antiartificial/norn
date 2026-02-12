# Deploying

Norn provides two paths for deploying applications: the CLI and the web UI. Both follow the same pipeline under the hood and provide real-time progress feedback over WebSocket.

## CLI Deploy

Deploy an application using a specific commit SHA:

```bash
norn deploy myapp abc123
```

Or deploy the latest commit from the configured branch:

```bash
norn deploy myapp HEAD
```

## Deploy Pipeline Steps

When you trigger a deploy, Norn executes the following steps in order:

### 1. Clone

Norn clones the Git repository (or creates a local copy fallback) into a temporary working directory and resolves the actual commit SHA. If you specified `HEAD`, this step determines the concrete SHA to deploy.

### 2. Build

Norn builds a Docker image using the Dockerfile specified in your infraspec:

```bash
docker build -t myapp:abc123 .
```

The image is then pushed to your configured registry or loaded directly into minikube for local development.

### 3. Test

If your infraspec defines a `build.test` command, Norn runs it in the working directory. This is your opportunity to run unit tests, linting, or other pre-deployment validation.

### 4. Snapshot

For applications with PostgreSQL databases, Norn creates a snapshot before proceeding:

```bash
pg_dump -Fc -d <database> -f snapshots/<db>_<sha>_<timestamp>.dump
```

This snapshot can be used to restore the database if migrations cause issues. Applications without databases skip this step.

### 5. Migrate

Norn runs the migration command specified in your infraspec's `migrations.command` field. This step applies any database schema changes needed for the new version.

### 6. Deploy

Norn updates the Kubernetes deployment to use the new image:

- For standard deployments: `SetImage` on the K8s deployment resource
- For cron jobs: Register the new version with the cron scheduler

### 7. Cleanup

Norn removes the temporary working directory, leaving only the built image and any generated snapshots.

## Reading Pipeline Output

Both the UI and CLI provide real-time progress feedback:

### CLI Output

The CLI displays spinners and step status indicators as each stage completes:

```
✓ Clone completed
⠋ Building image...
```

### UI Output

The web UI shows a deploy panel with visual step indicators. Each step transitions from pending to in-progress to complete as the pipeline advances.

## Pre-Deploy Checklist

Before you can deploy an application:

1. **Run Forge**: The app must be forged first to create Kubernetes resources and initial configuration
2. **Create Infraspec**: The app must have a valid `infraspec.yaml` defining build and deployment settings

Without these prerequisites, the deploy will fail early with a clear error message.
