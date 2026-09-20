variable "image" {
  type = string
}

# One-shot: apply the feedmap-pipeline inventory schema (migrations + grants) to
# the co-located Patroni PostgreSQL, using the owner role. See scripts/deploy-pipeline.
job "feedmap-pipeline-initdb" {
  datacenters = ["dc1"]
  type        = "batch"

  group "initdb" {
    count = 1

    restart {
      attempts = 0
      mode     = "fail"
    }

    task "initdb" {
      driver = "docker"

      config {
        # Image ENTRYPOINT is `feedmap-pipeline`; command/args are appended to it.
        image   = var.image
        command = "inventory"
        args    = ["init-db"]
      }

      template {
        destination = "secrets/db.env"
        env         = true
        change_mode = "noop"
        data        = <<EOH
{{ with nomadVar "nomad/jobs/feedmap-pipeline-initdb" }}
FEEDMAP_INVENTORY_OWNER_DSN={{ .OWNER_DSN }}
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
