variable "image" {
  type = string
}

# Direct-submit Nomad job for the feedmap-trinity STAGING deployment (see
# scripts/deploy-trinity). One image serves every process; the group's task
# command selects the role. Non-secret config is inline env; secret + managed-
# service connection values are rendered from the nomad var
# "nomad/jobs/feedmap-trinity" so they never live in this file or the registry.
#
# SAFETY: STAGING_WRITES_ENABLED=true + REMOTE_WRITES_ENABLED=false route derived
# feeds to the disposable Spaces bucket and keep every customer FTP/SFTP channel a
# local sink (config/filesystems.php + StagingRemoteWriteSafetyTest).
job "feedmap-trinity" {
  datacenters = ["dc1"]
  type        = "service"

  update {
    max_parallel      = 1
    canary            = 1
    auto_promote      = true
    auto_revert       = true
    min_healthy_time  = "10s"
    healthy_deadline  = "3m"
    progress_deadline = "5m"
  }

  # ---- HTTP tier (the only ported process) --------------------------------
  group "web" {
    count = 2

    spread {
      attribute = "${node.unique.name}"
      weight    = 100
    }

    network {
      port "http" {
        to = 8080
      }
    }

    service {
      name     = "feedmap-trinity"
      provider = "consul"
      port     = "http"

      check {
        name     = "http-ready"
        type     = "http"
        path     = "/api/readyz"
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

    task "web" {
      driver = "docker"

      config {
        image   = var.image
        ports   = ["http"]
        command = "php"
        args    = ["artisan", "serve", "--host=0.0.0.0", "--port=8080"]
      }

      env {
        APP_NAME              = "Feedmap Trinity (staging)"
        APP_ENV               = "staging"
        APP_DEBUG             = "false"
        LOG_CHANNEL           = "stack"
        LOG_LEVEL             = "info"
        STAGING_WRITES_ENABLED = "true"
        REMOTE_WRITES_ENABLED = "false"
        FILESYSTEM_DRIVER     = "local"
        DB_CONNECTION         = "mysql"
        DB_DATABASE           = "feedmap"
        DB_USERNAME           = "feedmap"
        CACHE_DRIVER          = "redis"
        QUEUE_CONNECTION      = "redis"
        SESSION_DRIVER        = "file"
        HEALTH_CHECK_DATABASE = "true"
        HEALTH_CHECK_REDIS    = "true"
      }

      template {
        destination = "secrets/app.env"
        env         = true
        change_mode = "restart"
        data        = <<EOH
{{ with nomadVar "nomad/jobs/feedmap-trinity" }}
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
        cpu    = 300
        memory = 384
      }
    }
  }

  # ---- Ingest worker (port-less) ------------------------------------------
  group "worker-ingest" {
    count = 1

    restart {
      attempts = 3
      interval = "5m"
      delay    = "15s"
      mode     = "delay"
    }

    task "worker" {
      driver = "docker"

      config {
        image   = var.image
        command = "php"
        args = ["artisan", "queue:work", "redis", "--queue=download",
          "--sleep=3", "--tries=3", "--timeout=3600", "--max-time=3600", "--no-interaction"]
      }

      env {
        APP_NAME              = "Feedmap Trinity (staging)"
        APP_ENV               = "staging"
        APP_DEBUG             = "false"
        LOG_CHANNEL           = "stack"
        LOG_LEVEL             = "info"
        STAGING_WRITES_ENABLED = "true"
        REMOTE_WRITES_ENABLED = "false"
        FILESYSTEM_DRIVER     = "local"
        DB_CONNECTION         = "mysql"
        DB_DATABASE           = "feedmap"
        DB_USERNAME           = "feedmap"
        CACHE_DRIVER          = "redis"
        QUEUE_CONNECTION      = "redis"
        SESSION_DRIVER        = "file"
      }

      template {
        destination = "secrets/app.env"
        env         = true
        change_mode = "restart"
        data        = <<EOH
{{ with nomadVar "nomad/jobs/feedmap-trinity" }}
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
        cpu    = 250
        memory = 384
      }
    }
  }

  # ---- Processing workers (port-less) -------------------------------------
  group "worker-process" {
    count = 2

    restart {
      attempts = 3
      interval = "5m"
      delay    = "15s"
      mode     = "delay"
    }

    task "worker" {
      driver = "docker"

      config {
        image   = var.image
        command = "php"
        args = ["artisan", "queue:work", "redis",
          "--queue=facebook,google,gmb,snapchat,tiktok,pinterest,default",
          "--sleep=3", "--tries=3", "--timeout=3600", "--max-time=3600", "--no-interaction"]
      }

      env {
        APP_NAME              = "Feedmap Trinity (staging)"
        APP_ENV               = "staging"
        APP_DEBUG             = "false"
        LOG_CHANNEL           = "stack"
        LOG_LEVEL             = "info"
        STAGING_WRITES_ENABLED = "true"
        REMOTE_WRITES_ENABLED = "false"
        FILESYSTEM_DRIVER     = "local"
        DB_CONNECTION         = "mysql"
        DB_DATABASE           = "feedmap"
        DB_USERNAME           = "feedmap"
        CACHE_DRIVER          = "redis"
        QUEUE_CONNECTION      = "redis"
        SESSION_DRIVER        = "file"
      }

      template {
        destination = "secrets/app.env"
        env         = true
        change_mode = "restart"
        data        = <<EOH
{{ with nomadVar "nomad/jobs/feedmap-trinity" }}
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
        cpu    = 250
        memory = 512
      }
    }
  }

  # ---- Laravel scheduler (port-less, single instance) ---------------------
  group "scheduler" {
    count = 1

    restart {
      attempts = 3
      interval = "5m"
      delay    = "15s"
      mode     = "delay"
    }

    task "scheduler" {
      driver = "docker"

      config {
        image   = var.image
        command = "php"
        args    = ["artisan", "schedule:work", "--no-interaction"]
      }

      env {
        APP_NAME              = "Feedmap Trinity (staging)"
        APP_ENV               = "staging"
        APP_DEBUG             = "false"
        LOG_CHANNEL           = "stack"
        LOG_LEVEL             = "info"
        STAGING_WRITES_ENABLED = "true"
        REMOTE_WRITES_ENABLED = "false"
        FILESYSTEM_DRIVER     = "local"
        DB_CONNECTION         = "mysql"
        DB_DATABASE           = "feedmap"
        DB_USERNAME           = "feedmap"
        CACHE_DRIVER          = "redis"
        QUEUE_CONNECTION      = "redis"
        SESSION_DRIVER        = "file"
      }

      template {
        destination = "secrets/app.env"
        env         = true
        change_mode = "restart"
        data        = <<EOH
{{ with nomadVar "nomad/jobs/feedmap-trinity" }}
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
        cpu    = 100
        memory = 256
      }
    }
  }
}
