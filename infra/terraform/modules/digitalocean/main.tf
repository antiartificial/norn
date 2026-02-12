terraform {
  required_providers {
    digitalocean = {
      source = "digitalocean/digitalocean"
    }
  }
}

resource "digitalocean_droplet" "node" {
  name      = var.name
  size      = var.size
  region    = var.region
  image     = "ubuntu-24-04-x64"
  user_data = var.cloud_init_rendered
  ssh_keys  = [var.ssh_key_id]
  tags      = ["norn", var.role]
}
