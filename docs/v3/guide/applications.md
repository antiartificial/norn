# Application definitions

Norn discovers `infraspec.yaml` beneath its configured application directory.
The specification ties source, build, process, health, resource, secret, and
dependency declarations to an application identity.

New applications default to `deploy: false`. Review and validate the definition
before enabling deployment. A minimal draft HTTP app looks like this:

```yaml
name: hello-world
deploy: false
repo:
  url: https://github.com/your-org/hello-world.git
  branch: main
  autoDeploy: false
build:
  dockerfile: Dockerfile
  test: go test ./...
primaryRegion: primary
regions:
  primary:
    nomadRegion: global
    datacenters: [dc1]
    trafficWeight: 100
processes:
  web:
    command: ./hello-world
    port: 8080
    health:
      path: /health
    scaling:
      min: 1
    resources:
      cpu: 200
      memory: 256
```

The Dockerfile must build the named executable, and the app must implement its
health endpoint. Adjust the region/datacenter and resources for the target.
One allocation does not provide redundancy. An ingress endpoint, encrypted
secrets, and database bindings require their own reviewed configuration.

An app can also declare workers, scheduled jobs, and functions. Those process
types have different placement and execution rules. Resource declarations do
not themselves qualify online pool resizing or stateful migration.

## Source and deployment intent

Keep application code in the repository selected by the InfraSpec. Git-driven
changes can enter through the configured webhook and deployment controls.
Use a pushed commit SHA when rehearsing or deploying a release; a branch name
or `HEAD` can resolve differently later.

The deployment gate controls whether runtime changes are allowed. It is distinct
from `repo.autoDeploy`, which controls the configured automatic deployment path.
A disabled draft remains inspectable and preflightable.

See the [operator workflow](../operations/workflow.md) for deployment steps.
The [detailed InfraSpec field reference](../../v2/guide/infraspec-reference.md)
is retained under its existing URL. Its individual fields and process types
remain relevant; release-strategy and Fleet qualification claims are defined
by the V3 documentation.
