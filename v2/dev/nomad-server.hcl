data_dir = "/tmp/nomad-server"
bind_addr = "0.0.0.0"

advertise {
  http = "100.89.46.50"
  rpc  = "100.89.46.50"
  serf = "100.89.46.50"
}

server {
  enabled          = true
  bootstrap_expect = 1
}

client {
  enabled           = true
  cpu_total_compute = 4000

  host_volume "gitea-data" {
    path      = "/Users/0xadb/volumes/gitea-data"
    read_only = false
  }

  host_volume "signal-sideband-media" {
    path      = "/Users/0xadb/volumes/signal-sideband-media"
    read_only = false
  }

  host_volume "signal-cli-data" {
    path      = "/Users/0xadb/volumes/signal-cli-data"
    read_only = false
  }

  host_volume "garage-meta" {
    path      = "/Users/0xadb/volumes/garage-meta"
    read_only = false
  }

  host_volume "garage-data" {
    path      = "/Users/0xadb/volumes/garage-data"
    read_only = false
  }

  host_volume "redpanda-data" {
    path      = "/Users/0xadb/volumes/redpanda-data"
    read_only = false
  }
}

plugin "docker" {}
