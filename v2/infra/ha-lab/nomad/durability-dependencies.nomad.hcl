# Test-only external dependencies for the Norn durability pilot. The app is
# deployed separately by Norn from durability-pilot/infraspec.yaml; it reaches
# these services solely through Consul DNS. Neither service is exposed through
# the public load balancer.
#
# This intentionally uses one Valkey and one Redpanda broker to prove an
# application's outbox, idempotency, readiness, and recovery semantics. It is
# not a cache or broker HA topology. Use replicated, dedicated external
# services before production.

variable "valkey_image" {
  type = string
}

variable "redpanda_image" {
  type = string
}

job "norn-durability-dependencies" {
  datacenters = ["dc1"]
  type        = "service"

  group "valkey" {
    count = 1

    network {
      mode = "host"
      port "valkey" {
        static = 16379
      }
    }

    service {
      name     = "norn-durability-valkey"
      provider = "consul"
      port     = "valkey"

      check {
        name     = "valkey-tcp"
        type     = "tcp"
        interval = "10s"
        timeout  = "2s"
      }
    }

    restart {
      attempts = 5
      interval = "10m"
      delay    = "10s"
      mode     = "delay"
    }

    task "valkey" {
      driver = "docker"

      config {
        image = var.valkey_image
        args = [
          "valkey-server",
          "--port", "16379",
          "--appendonly", "yes",
          "--appendfsync", "everysec",
          "--save", "60", "1",
          "--dir", "/data",
        ]
        volumes = ["/var/lib/norn-durability/valkey:/data"]
      }

      resources {
        cpu    = 250
        memory = 256
      }
    }
  }

  group "redpanda" {
    count = 1

    network {
      mode = "host"
      port "kafka" {
        static = 19092
      }
      port "admin" {
        static = 19644
      }
    }

    service {
      name     = "norn-durability-redpanda"
      provider = "consul"
      port     = "kafka"

      check {
        name     = "redpanda-ready"
        type     = "http"
        port     = "admin"
        path     = "/v1/status/ready"
        interval = "10s"
        timeout  = "3s"
      }
    }

    restart {
      attempts = 5
      interval = "10m"
      delay    = "10s"
      mode     = "delay"
    }

    task "redpanda" {
      driver = "docker"

      config {
        image   = var.redpanda_image
        command = "redpanda"
        args = [
          "start",
          "--kafka-addr", "PLAINTEXT://0.0.0.0:19092",
          "--advertise-kafka-addr", "PLAINTEXT://${NOMAD_IP_kafka}:19092",
          "--admin-addr", "0.0.0.0:19644",
          "--advertise-admin-addr", "${NOMAD_IP_admin}:19644",
          "--mode", "dev-container",
          "--smp", "1",
          "--default-log-level=info",
        ]
        volumes = ["/var/lib/norn-durability/redpanda:/var/lib/redpanda/data"]
      }

      resources {
        cpu    = 1000
        memory = 2048
      }
    }
  }
}
