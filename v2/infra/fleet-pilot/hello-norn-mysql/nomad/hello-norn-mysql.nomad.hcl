# Direct Nomad-only staging pilot. Keep the Norn catalog declaration disabled
# until its translator can render Nomad-variable-backed secret files.
variable "image" {
  type        = string
  description = "Signed, reviewed OCI image manifest digest."
}

variable "source_version" {
  type        = string
  description = "Exact source revision baked into the image's /version response."
}

variable "hostname" {
  type        = string
  description = "Reviewed public pilot hostname; the DO LB passes TLS through to Traefik :443."
}

job "hello-norn-mysql" {
  datacenters = ["nyc3"]
  node_pool   = "ingress"
  type        = "service"

  meta {
    pilot_image          = var.image
    pilot_source_version = var.source_version
    pilot_hostname       = var.hostname
  }

  update {
    max_parallel      = 1
    min_healthy_time  = "15s"
    healthy_deadline  = "3m"
    progress_deadline = "10m"
    auto_revert       = true
  }

  group "web" {
    count = 2
    # Let Consul/Traefik withdraw a draining allocation before SIGTERM; the
    # task then receives its configured 20-second kill timeout.
    shutdown_delay = "10s"

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
      # These scheduler-owned values make the Consul registration
      # independently correlatable with the exact Nomad allocation. They are
      # evidence only; routing continues to use the service name and checks.
      meta {
        norn_alloc_id  = "${NOMAD_ALLOC_ID}"
        norn_node_id   = "${node.unique.id}"
        norn_job_id    = "${NOMAD_JOB_ID}"
        norn_namespace = "${NOMAD_NAMESPACE}"
        norn_region    = "${node.region}"
      }
      tags = [
        "traefik.enable=true",
        "traefik.http.routers.hello-norn-mysql.rule=Host(`${var.hostname}`) && (PathPrefix(`/records/`) || Path(`/version`))",
        "traefik.http.routers.hello-norn-mysql.entrypoints=websecure",
        "traefik.http.routers.hello-norn-mysql.tls=true",
      ]

      # Readiness controls Consul/Traefik admission. The public router does
      # not expose this path.
      check {
        name     = "readyz"
        type     = "http"
        path     = "/readyz"
        interval = "5s"
        timeout  = "3s"
      }

      # Liveness asks Nomad to restart a wedged task without treating MySQL
      # readiness as a restart condition.
      check {
        name     = "healthz"
        type     = "http"
        path     = "/healthz"
        interval = "10s"
        timeout  = "3s"

        check_restart {
          limit = 3
          grace = "30s"
        }
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
        # These are allocation file paths, never secret values.
        MYSQL_DSN_FILE         = "${NOMAD_SECRETS_DIR}/mysql-dsn"
        MYSQL_CA_FILE          = "${NOMAD_SECRETS_DIR}/mysql-ca.pem"
        MYSQL_PINNED_IP_FILE   = "${NOMAD_SECRETS_DIR}/mysql-pinned-ip"
        PILOT_WRITE_TOKEN_FILE = "${NOMAD_SECRETS_DIR}/pilot-write-token"
      }

      # Runtime identity: SELECT, INSERT and UPDATE on pilot_records only.
      template {
        destination          = "secrets/mysql-dsn"
        change_mode          = "restart"
        perms                = "0400"
        uid                  = 65532
        gid                  = 65532
        error_on_missing_key = true
        data = <<-EOT
{{ with nomadVar "nomad/jobs/hello-norn-mysql" }}
{{ .MYSQL_DSN.Value }}
{{ end }}
EOT
      }

      template {
        destination          = "secrets/mysql-pinned-ip"
        change_mode          = "restart"
        perms                = "0400"
        uid                  = 65532
        gid                  = 65532
        error_on_missing_key = true
        data = <<-EOT
{{ with nomadVar "nomad/jobs/hello-norn-mysql" }}
{{ .MYSQL_PINNED_IP.Value }}
{{ end }}
EOT
      }

      template {
        destination          = "secrets/pilot-write-token"
        change_mode          = "restart"
        perms                = "0400"
        uid                  = 65532
        gid                  = 65532
        error_on_missing_key = true
        data = <<-EOT
{{ with nomadVar "nomad/jobs/hello-norn-mysql" }}
{{ .PILOT_WRITE_TOKEN.Value }}
{{ end }}
EOT
      }

      # The provider CA is a separate, reviewed non-credential variable. It
      # is never parsed as dotenv and therefore may safely remain multiline.
      template {
        destination          = "secrets/mysql-ca.pem"
        change_mode          = "restart"
        perms                = "0400"
        uid                  = 65532
        gid                  = 65532
        error_on_missing_key = true
        data = <<-EOT
{{ with nomadVar "nomad/jobs/hello-norn-mysql" }}
{{ .MYSQL_CA_PEM.Value }}
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
