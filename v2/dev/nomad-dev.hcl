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

  host_volume "ft-bookmarks" {
    path      = "/Users/0xadb/.ft-bookmarks"
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
