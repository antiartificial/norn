variable "image" {
  type = string
}

# One-shot database migration for feedmap-trinity, run to completion before the
# service job (see scripts/deploy-trinity). Same image + nomad var as the service.
job "feedmap-trinity-migrate" {
  datacenters = ["dc1"]
  type        = "batch"

  group "migrate" {
    count = 1

    restart {
      attempts = 2
      interval = "5m"
      delay    = "15s"
      mode     = "fail"
    }

    reschedule {
      attempts  = 1
      interval  = "10m"
      unlimited = false
    }

    task "migrate" {
      driver = "docker"

      config {
        image   = var.image
        command = "php"
        args    = ["artisan", "migrate", "--force", "--no-interaction"]
      }

      env {
        APP_ENV       = "staging"
        APP_DEBUG     = "false"
        LOG_CHANNEL   = "stderr"
        LOG_LEVEL     = "info"
        DB_CONNECTION = "mysql"
        DB_DATABASE   = "feedmap"
        DB_USERNAME   = "feedmap"
        CACHE_DRIVER  = "array"
        SESSION_DRIVER = "array"
        QUEUE_CONNECTION = "sync"
      }

      template {
        destination = "secrets/app.env"
        env         = true
        change_mode = "noop"
        data        = <<EOH
{{ with nomadVar "nomad/jobs/feedmap-trinity" }}
APP_KEY={{ .APP_KEY }}
DB_HOST={{ .DB_HOST }}
DB_PORT={{ .DB_PORT }}
DB_PASSWORD={{ .DB_PASSWORD }}
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
