variable "image" {
  type = string
}

# feedmap-pipeline read-only inventory API. The server binds 127.0.0.1:8765
# inside the container by design (local read-only), so it is reached via
# `nomad alloc exec ... curl 127.0.0.1:8765`, not the regional ingress.
job "feedmap-pipeline-api" {
  datacenters = ["dc1"]
  type        = "service"

  group "api" {
    count = 1

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
