# Volumes

Norn v2 uses Nomad host volumes for persistent storage. This replaces Kubernetes PersistentVolumeClaims from v1.

## How It Works

Host volumes are directories on Nomad client nodes that are mounted into task containers. They persist across job restarts and redeployments.

## Prerequisites

### 1. Create the directory on the Nomad client

```bash
sudo mkdir -p /opt/volumes/signal-data
sudo chown nomad:nomad /opt/volumes/signal-data
```

### 2. Add a host_volume stanza to the Nomad client config

```hcl
# /etc/nomad.d/client.hcl
client {
  enabled = true

  host_volume "signal-data" {
    path      = "/opt/volumes/signal-data"
    read_only = false
  }
}
```

Restart the Nomad client after adding the stanza.

### 3. Declare the volume in infraspec.yaml

```yaml
volumes:
  - name: signal-data
    mount: /var/lib/signal-cli
    readOnly: false
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | string | — | Must match the Nomad `host_volume` name |
| `mount` | string | — | Mount path inside the container |
| `readOnly` | bool | `false` | Mount as read-only |

## Nomad Translation

The translator maps infraspec volumes to Nomad job config:

```hcl
group "web" {
  volume "signal-data" {
    type   = "host"
    source = "signal-data"
  }

  task "web" {
    volume_mount {
      volume      = "signal-data"
      destination = "/var/lib/signal-cli"
      read_only   = false
    }
  }
}
```

Volumes are attached to every TaskGroup in the job, including periodic and batch jobs.

## Use Case: signal-cli Storage

The signal-sideband app uses a host volume to persist signal-cli's registration data and message store:

```yaml
# signal-sideband/infraspec.yaml
volumes:
  - name: signal-data
    mount: /var/lib/signal-cli
```

This ensures signal-cli's identity and message database survive container restarts.

## Multiple Volumes

An app can declare multiple volumes:

```yaml
volumes:
  - name: app-data
    mount: /data
  - name: app-cache
    mount: /cache
    readOnly: false
```

Each volume needs a matching `host_volume` stanza on the Nomad client.
