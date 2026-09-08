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
      tags = [
        "traefik.enable=true",
        "traefik.http.routers.hello-norn-mysql.rule=Host(`${var.hostname}`) && (PathPrefix(`/records/`) || Path(`/version`))",
        "traefik.http.routers.hello-norn-mysql.entrypoints=websecure",
        "traefik.http.routers.hello-norn-mysql.tls=true",
        "traefik.http.services.hello-norn-mysql.loadbalancer.server.port=8080",
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
        # These are paths, not secret values. Nomad renders the DSN only into
        # the process environment from the ACL-restricted runtime variable.
        MYSQL_CA_FILE = "${NOMAD_SECRETS_DIR}/mysql-ca.pem"
      }

      # Runtime identity: SELECT, INSERT and UPDATE on pilot_records only.
      # toJSON preserves a DSN containing punctuation as one dotenv value.
      template {
        destination          = "secrets/mysql-runtime.env"
        env                  = true
        change_mode          = "restart"
        perms                = "0400"
        uid                  = 65532
        gid                  = 65532
        error_on_missing_key = true
        data = <<-EOT
{{ with nomadVar "nomad/jobs/hello-norn-mysql/runtime" }}
MYSQL_DSN={{ .MYSQL_DSN | toJSON }}
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
{{ with nomadVar "nomad/pilot/hello-norn-mysql/provider-ca" }}
{{ .PEM }}
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
