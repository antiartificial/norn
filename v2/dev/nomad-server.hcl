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
}

plugin "docker" {}
