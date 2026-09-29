variable "image" {
  type = string
}

# feedmap-pipeline read-only inventory API.
#
# Gateway/VPC-ready: the server binds 0.0.0.0:8765 (FEEDMAP_INVENTORY_BIND) with
# remote access allowed (FEEDMAP_INVENTORY_ALLOW_REMOTE), the port is published
# on the node, and the task registers a Consul service — so it is reachable over
# the VPC (node private IP : dynamic port) and can be fronted by Traefik/the LB.
# Still default-closed at the app level unless those two env vars are set. For a
# real exposure, also set FEEDMAP_CONSOLE_TOKEN so authenticated routes need a token.
job "feedmap-pipeline-api" {
  datacenters = ["dc1"]
  type        = "service"

  group "api" {
    count = 1

    network {
      port "http" {
        to = 8765
      }
    }

    service {
      name     = "feedmap-pipeline-api"
      provider = "consul"
      port     = "http"

      check {
        name     = "http-healthz"
        type     = "http"
        path     = "/healthz"
        interval = "10s"
        timeout  = "2s"
      }
    }

    restart {
      attempts = 3
      interval = "5m"
      delay    = "10s"
      mode     = "delay"
    }

    task "api" {
      driver = "docker"

      config {
        # Image ENTRYPOINT is `feedmap-pipeline`; command/args are appended to it.
        image   = var.image
        command = "inventory"
        args    = ["serve", "--port", "8765"]
        ports   = ["http"]
      }

      env {
        FEEDMAP_INVENTORY_BIND         = "0.0.0.0"
        FEEDMAP_INVENTORY_ALLOW_REMOTE = "true"
      }

      template {
        destination = "secrets/db.env"
        env         = true
        change_mode = "restart"
        data        = <<EOH
{{ with nomadVar "nomad/jobs/feedmap-pipeline-api" }}
FEEDMAP_INVENTORY_READ_DSN={{ .READ_DSN }}
{{ end }}
EOH
      }

      resources {
        cpu    = 200
        memory = 256
      }
    }
  }
}
