job "norn-traefik" {
  datacenters = ["dc1"]
  type        = "system"

  group "ingress" {
    network {
      port "web" {
        static = 18080
        to     = 8080
      }
    }

    service {
      name     = "norn-traefik"
      provider = "consul"
      port     = "web"

      check {
        name     = "ping"
        type     = "http"
        path     = "/ping"
        interval = "10s"
        timeout  = "3s"
      }
    }

    task "traefik" {
      driver = "docker"

      config {
        image = "traefik@sha256:e157892efcf505bb9b3e5c79011ebf398e5f583acd240572e1a47f0b2044c74a"
        ports = ["web"]
        args  = ["--configfile=/local/traefik.yml"]
        volumes = [
          "/etc/consul.d/pki:/consul-pki:ro",
        ]
      }

      template {
        destination = "local/traefik.yml"
        perms       = "0600"
        change_mode = "restart"
        data = <<EOH
entryPoints:
  web:
    address: ":8080"
ping:
  entryPoint: web
accessLog:
  format: json
metrics:
  prometheus:
    entryPoint: web
providers:
  consulCatalog:
    exposedByDefault: false
    prefix: traefik
    watch: true
    strictChecks: [passing]
    endpoint:
      address: '{{ env "NOMAD_IP_web" }}:8501'
      scheme: https
      token: '{{ with nomadVar "nomad/jobs/norn-traefik" }}{{ .consul_token }}{{ end }}'
      tls:
        ca: /consul-pki/ca.pem
        cert: /consul-pki/consul.pem
        key: /consul-pki/consul-key.pem
EOH
      }

      resources {
        cpu    = 150
        memory = 128
      }
    }
  }
}
