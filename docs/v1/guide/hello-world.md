# Hello World

End-to-end walkthrough: create an app, write a Dockerfile, add an infraspec, forge infrastructure, deploy, and visit it live.

## What you'll build

A simple HTTP server that responds with "Hello from Norn!" — discovered, forged, and deployed through Norn.

## Prerequisites

- Norn running locally (`make dev`)
- Docker installed
- CLI installed (`make install`)

## 1. Create the app

```bash
mkdir -p ~/projects/hello-norn
cd ~/projects/hello-norn
```

## 2. Write the server

Create `main.go`:

```go
package main

import (
    "fmt"
    "net/http"
)

func main() {
    http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        fmt.Fprintln(w, "Hello from Norn!")
    })
    http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(200)
    })
    http.ListenAndServe(":3000", nil)
}
```

## 3. Add a Dockerfile

```dockerfile
FROM golang:1.25-alpine AS build
WORKDIR /app
COPY . .
RUN go build -o server .

FROM alpine:3.21
COPY --from=build /app/server /server
EXPOSE 3000
CMD ["/server"]
```

## 4. Create the infraspec

Create `infraspec.yaml`:

```yaml
app: hello-norn
role: webserver
port: 3000
healthcheck: /health
deploy: true
build:
  dockerfile: Dockerfile
```

## 5. Verify discovery

```bash
norn status
```

You should see `hello-norn` listed with "never deployed".

## 6. Forge infrastructure

```bash
norn forge hello-norn
```

This creates the Kubernetes Deployment and Service. For cron or function apps, it just registers them.

## 7. Deploy

```bash
norn deploy hello-norn HEAD
```

Watch the seven-step pipeline:

1. **clone** — copy source files
2. **build** — `docker build -t hello-norn:latest .`
3. **test** — skipped (no test command)
4. **snapshot** — skipped (no database)
5. **migrate** — skipped (no migrations)
6. **deploy** — update K8s deployment image
7. **cleanup** — remove temp files

## 8. Visit

Open the dashboard at [localhost:5173](http://localhost:5173) — `hello-norn` should show a green health dot.

From the CLI:

```bash
norn status hello-norn   # detailed status
norn logs hello-norn     # stream logs
norn health              # check all services
```

## Next steps

- [Concepts](/v1/guide/concepts) — understand roles, services, and forge vs deploy
- [Infraspec Reference](/v1/guide/infraspec-reference) — all configuration fields
- [Roles Guide](/v1/guide/roles) — webserver, worker, cron, function examples
