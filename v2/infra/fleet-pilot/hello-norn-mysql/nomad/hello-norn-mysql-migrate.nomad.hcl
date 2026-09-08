# One-shot migration job. Submit it only with the separately ACL-restricted
# migration variable, then remove that variable and its policy binding.
variable "image" {
  type = string
}

job "hello-norn-mysql-migrate" {
  datacenters = ["nyc3"]
  node_pool   = "ingress"
  type        = "batch"

  group "migrate" {
    count = 1

    restart {
      attempts = 0
      mode     = "fail"
    }

    reschedule {
      attempts  = 0
      unlimited = false
    }

    task "migrate" {
      driver = "docker"

      config {
        image = var.image
        args  = ["migrate"]
      }

      env {
        MYSQL_CA_FILE = "${NOMAD_SECRETS_DIR}/mysql-ca.pem"
      }

      # Migration identity has CREATE only for pilot_records plus the runtime
      # table privileges. It exists for this one job, never the web job.
      template {
        destination          = "secrets/mysql-migration.env"
        env                  = true
        change_mode          = "restart"
        perms                = "0400"
        uid                  = 65532
        gid                  = 65532
        error_on_missing_key = true
        data = <<-EOT
{{ with nomadVar "nomad/jobs/hello-norn-mysql/migration" }}
MYSQL_DSN={{ .MYSQL_DSN | toJSON }}
{{ end }}
EOT
      }

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

      resources {
        cpu    = 100
        memory = 128
      }
    }
  }
}
