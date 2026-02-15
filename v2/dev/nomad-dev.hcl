client {
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
}
