variable "image" {
  type = string
}

# Shell command to run, e.g. "php artisan db:seed --force" or a chain like
# "php artisan parser:observe 5 && php artisan feeds:publish-staging 5".
# A chain matters because parse writes to the alloc's local storage/app and
# feeds:publish-staging reads it — they must run in the same allocation.
variable "cmd" {
  type = string
}

# One-shot artisan runner for feedmap-trinity staging operations (seed, ingest,
# feeds:publish-staging, etc). Full staging env + all secrets so any command
# works. Reads its own nomad var path (workload identity scopes var access to
# nomad/jobs/<job-id>). See scripts/deploy-trinity `artisan`.
job "feedmap-trinity-oneshot" {
  datacenters = ["dc1"]
  type        = "batch"

  group "oneshot" {
    count = 1

    restart {
      attempts = 0
      mode     = "fail"
    }

    reschedule {
      attempts  = 0
      unlimited = false
    }

    task "oneshot" {
      driver = "docker"

      config {
        image   = var.image
        command = "sh"
        args    = ["-lc", var.cmd]
      }

      env {
        APP_NAME               = "Feedmap Trinity (staging)"
        APP_ENV                = "staging"
        APP_DEBUG              = "false"
        LOG_CHANNEL            = "stderr"
        LOG_LEVEL              = "info"
        STAGING_WRITES_ENABLED = "true"
        REMOTE_WRITES_ENABLED  = "false"
        FILESYSTEM_DRIVER      = "local"
        DB_CONNECTION          = "mysql"
        DB_DATABASE            = "feedmap"
        DB_USERNAME            = "feedmap"
        CACHE_DRIVER           = "array"
        QUEUE_CONNECTION       = "sync"
        SESSION_DRIVER         = "array"
        HEALTH_CHECK_DATABASE  = "false"
        HEALTH_CHECK_REDIS     = "false"
      }

      template {
        destination = "secrets/app.env"
        env         = true
        change_mode = "noop"
        data        = <<EOH
{{ with nomadVar "nomad/jobs/feedmap-trinity-oneshot" }}
APP_KEY={{ .APP_KEY }}
DB_HOST={{ .DB_HOST }}
DB_PORT={{ .DB_PORT }}
DB_PASSWORD={{ .DB_PASSWORD }}
REDIS_HOST={{ .REDIS_HOST }}
REDIS_PORT={{ .REDIS_PORT }}
REDIS_PASSWORD={{ .REDIS_PASSWORD }}
DIGITALOCEAN_SPACES_KEY={{ .DIGITALOCEAN_SPACES_KEY }}
DIGITALOCEAN_SPACES_SECRET={{ .DIGITALOCEAN_SPACES_SECRET }}
DIGITALOCEAN_SPACES_BUCKET={{ .DIGITALOCEAN_SPACES_BUCKET }}
DIGITALOCEAN_SPACES_REGION={{ .DIGITALOCEAN_SPACES_REGION }}
DIGITALOCEAN_SPACES_ENDPOINT={{ .DIGITALOCEAN_SPACES_ENDPOINT }}
{{ end }}
EOH
      }

      resources {
        cpu    = 400
        memory = 512
      }
    }
  }
}
