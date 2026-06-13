job "redpanda" {
  datacenters = ["dc1"]
  type        = "service"

  group "broker" {
    count = 1

    network {
      port "kafka" {
        static = 9092
      }
      port "admin" {
        static = 9644
      }
      port "schema" {
        static = 8081
      }
      port "proxy" {
        static = 8082
      }
    }

    volume "redpanda-data" {
      type      = "host"
      source    = "redpanda-data"
      read_only = false
    }

    service {
      name = "redpanda"
      port = "kafka"

      check {
        type     = "tcp"
        port     = "kafka"
        interval = "10s"
        timeout  = "2s"
      }
    }

    service {
      name = "redpanda-admin"
      port = "admin"

      check {
        type     = "http"
        path     = "/v1/status/ready"
        interval = "10s"
        timeout  = "2s"
      }
    }

    task "redpanda" {
      driver = "docker"

      config {
        image = "docker.redpanda.com/redpandadata/redpanda:v26.1.10"
        ports = ["kafka", "admin", "schema", "proxy"]
        args = [
          "redpanda",
          "start",
          "--mode", "dev-container",
          "--smp", "1",
          "--memory", "1G",
          "--reserve-memory", "0M",
          "--overprovisioned",
          "--node-id", "0",
          "--check=false",
          "--kafka-addr", "internal://0.0.0.0:9092",
          "--advertise-kafka-addr", "internal://${NOMAD_IP_kafka}:${NOMAD_PORT_kafka}",
          "--admin-addr", "0.0.0.0:9644",
          "--schema-registry-addr", "0.0.0.0:8081",
          "--pandaproxy-addr", "0.0.0.0:8082"
        ]
      }

      volume_mount {
        volume      = "redpanda-data"
        destination = "/var/lib/redpanda/data"
        read_only   = false
      }

      resources {
        cpu    = 500
        memory = 1536
      }
    }
  }
}
