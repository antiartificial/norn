terraform {
  required_providers {
    hcloud = {
      source = "hetznercloud/hcloud"
    }
  }
}

resource "hcloud_server" "node" {
  name        = var.name
  server_type = var.size
  location    = var.region
  image       = "ubuntu-24.04"
  user_data   = var.cloud_init_rendered
  ssh_keys    = [var.ssh_key_id]
  labels      = { managed-by = "norn", role = var.role }
}
