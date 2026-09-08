# Direct Nomad rehearsal job for a fleet that has already completed the
# norn-fleet bootstrap. Norn normally translates the reviewed infraspec into
# an equivalent job; submit exactly one of the two paths, never both.
variable "image" {
  type        = string
  description = "Reviewed signed OCI image reference pinned by manifest digest."
}

job "hello-norn-mysql" {
  datacenters = ["nyc3"]
  node_pool   = "ingress"
  type        = "service"

  update {
    max_parallel      = 1
    min_healthy_time  = "15s"
    healthy_deadline  = "3m"
    progress_deadline = "10m"
    auto_revert       = true
  }

  group "web" {
    count = 2

    # This is a hard scheduler constraint, not merely a preference: the
    # pilot's availability measurement needs its two allocations on clients
    # with independent failure domains.
    constraint {
      operator = "distinct_hosts"
    }

    network {
      port "http" {
        to = 8080
      }
    }

    service {
      name     = "hello-norn-mysql-web"
      provider = "consul"
      port     = "http"

      check {
        name     = "readyz"
        type     = "http"
        path     = "/readyz"
        interval = "5s"
        timeout  = "3s"
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

      # The Nomad ACL policy must allow this job to read exactly this variable
      # path. Values are materialized in the allocation only and never appear
      # in this catalog, job status, or exercise evidence.
      template {
        destination = "secrets/mysql.env"
        env         = true
        change_mode = "restart"
        data = <<-EOT
{{ with nomadVar "nomad/jobs/hello-norn-mysql" }}
MYSQL_DSN={{ .MYSQL_DSN }}
MYSQL_CA_PEM={{ .MYSQL_CA_PEM }}
{{ end }}
EOT
      }

      kill_signal  = "SIGTERM"
      kill_timeout = "20s"

      resources {
        cpu    = 200
        memory = 128
      }
    }
  }
}
