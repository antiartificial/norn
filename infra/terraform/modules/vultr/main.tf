terraform {
  required_providers {
    vultr = {
      source = "vultr/vultr"
    }
  }
}

resource "vultr_instance" "node" {
  label       = var.name
  hostname    = var.name
  plan        = var.size
  region      = var.region
  os_id       = 2284 # Ubuntu 24.04
  user_data   = var.cloud_init_rendered
  ssh_key_ids = [var.ssh_key_id]
  tags        = ["norn", var.role]
}
