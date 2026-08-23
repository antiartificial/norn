variable "image" {
  type = string
}

variable "version" {
  type = string
}

job "norn-ha-toy" {
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
      name     = "norn-ha-toy"
      provider = "consul"
      port     = "http"

      check {
        name     = "http-health"
        type     = "http"
        path     = "/health"
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
        image = var.image
        ports = ["http"]
      }

      env {
        APP_VERSION = var.version
      }

      template {
        destination = "secrets/database.env"
        env         = true
        change_mode = "restart"
        data = <<EOH
{{ with nomadVar "nomad/jobs/norn-ha-toy" }}
DATABASE_URL=postgres://norn_test:{{ .database_password }}@{{ env "NOMAD_IP_http" }}:6432/norn_test?sslmode=require
{{ end }}
EOH
      }

      resources {
        cpu    = 200
        memory = 128
      }
    }
  }
}
