# Roles

Every app has a `role` that determines how Norn handles its infrastructure and lifecycle. There are four roles: **webserver**, **worker**, **cron**, and **function**.

## webserver

Long-running HTTP server with a Kubernetes Deployment, Service, health checks, and optional Cloudflare tunnel routing.

### infraspec

```yaml
app: mail-agent
role: webserver
port: 80
healthcheck: /health
hosts:
  external: mail.slopistry.com
  internal: mail-agent-service
build:
  dockerfile: Dockerfile
  test: npm test
services:
  postgres:
    database: mailagent_db
artifacts:
  retain: 5
```

### Dockerfile

```dockerfile
FROM node:20-alpine
WORKDIR /app
COPY package*.json ./
RUN npm ci --production
COPY . .
EXPOSE 80
CMD ["node", "server.js"]
```

### Forge creates

- K8s Deployment (image, replicas, health probe, env vars, secrets, volumes)
- K8s Service (maps internal hostname to pod port)
- Cloudflare tunnel ingress rule (if `hosts.external` set)
- DNS record via `cloudflared tunnel route dns`

---

## worker

Long-running background process. Gets a K8s Deployment but **no Service or ingress**. No healthcheck.

### infraspec

```yaml
app: queue-processor
role: worker
build:
  dockerfile: Dockerfile
services:
  events:
    topics: [jobs.pending, jobs.completed]
  kv:
    namespace: queue-processor
```

### Dockerfile

```dockerfile
FROM golang:1.25-alpine AS build
WORKDIR /app
COPY . .
RUN go build -o worker .

FROM alpine:3.21
COPY --from=build /app/worker /worker
CMD ["/worker"]
```

### Forge creates

- K8s Deployment (no port, no health probe)
- No Service or tunnel routing

---

## cron

Scheduled task. No K8s Deployment — Norn's built-in scheduler runs the container on a cron schedule using Docker (or Incus).

### infraspec

```yaml
app: daily-report
role: cron
schedule: "0 9 * * *"
command: node generate-report.js
timeout: 300
build:
  dockerfile: Dockerfile
services:
  postgres:
    database: reports_db
```

### Dockerfile

```dockerfile
FROM node:20-alpine
WORKDIR /app
COPY . .
RUN npm ci --production
CMD ["node", "generate-report.js"]
```

### How it works

1. **Forge** registers the cron schedule (no K8s resources created)
2. **Deploy** builds the image and registers it with the scheduler
3. Norn's scheduler runs the container at the specified interval
4. Each execution is tracked in the `cron_executions` table
5. Results are visible in the UI's Cron Panel and via `norn status <app>`

### Controls

- **Run Now** — trigger immediate execution
- **Pause/Resume** — stop/start the schedule
- **Update Schedule** — change the cron expression at runtime

---

## function

HTTP-triggered ephemeral container. Like cron, but invoked on-demand rather than on a schedule.

### infraspec

```yaml
app: thumbnail-gen
role: function
build:
  dockerfile: Dockerfile
function:
  timeout: 30
  trigger: http
  memory: 256m
```

### Dockerfile

```dockerfile
FROM golang:1.25-alpine AS build
WORKDIR /app
COPY . .
RUN go build -o handler .

FROM alpine:3.21
COPY --from=build /app/handler /handler
CMD ["/handler"]
```

### How it works

1. **Forge** registers the function (no K8s resources)
2. **Deploy** builds the image
3. **Invoke** runs the container with request data as environment variables:
   - `NORN_REQUEST_BODY` — the request body
   - `NORN_REQUEST_METHOD` — HTTP method
   - `NORN_REQUEST_PATH` — request path
4. Container stdout becomes the response
5. Executions tracked in `func_executions` table

### Invoke

From the UI: Click **Invoke** on the function's app card, enter a request body, and see the result.

From the CLI:

```bash
norn invoke thumbnail-gen --body '{"url": "https://example.com/photo.jpg"}'
norn invoke thumbnail-gen --body @request.json
```

---

## Role comparison

| | webserver | worker | cron | function |
|--|-----------|--------|------|----------|
| **K8s Deployment** | Yes | Yes | No | No |
| **K8s Service** | Yes | No | No | No |
| **Health checks** | Yes | No | No | No |
| **Cloudflare tunnel** | Optional | No | No | No |
| **Trigger** | Always running | Always running | Schedule | HTTP request |
| **Container lifecycle** | Long-running | Long-running | Ephemeral | Ephemeral |
| **Scaling** | Replicas | Replicas | N/A | N/A |
