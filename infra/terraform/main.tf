terraform {
  required_providers {
    hcloud = {
      source = "hetznercloud/hcloud"
    }
    digitalocean = {
      source = "digitalocean/digitalocean"
    }
    vultr = {
      source = "vultr/vultr"
    }
  }
}

provider "hcloud" {
  token = var.hcloud_token
}

provider "digitalocean" {
  token = var.do_token
}

provider "vultr" {
  api_key = var.vultr_api_key
}

# ---------------------------------------------------------------------------
# Locals: split nodes by provider, identify the first server
# ---------------------------------------------------------------------------

locals {
  hetzner_nodes      = { for k, v in var.nodes : k => v if v.provider == "hetzner" }
  digitalocean_nodes = { for k, v in var.nodes : k => v if v.provider == "digitalocean" }
  vultr_nodes        = { for k, v in var.nodes : k => v if v.provider == "vultr" }

  first_server_name = [for k, v in var.nodes : k if v.role == "server"][0]
}

# ---------------------------------------------------------------------------
# Cloud-init: render once per node
# ---------------------------------------------------------------------------

module "cloud_init" {
  source   = "./modules/k3s-node"
  for_each = var.nodes

  role               = each.value.role
  node_name          = each.key
  k3s_token          = var.k3s_token
  tailscale_auth_key = var.tailscale_auth_key
  is_first_server    = each.key == local.first_server_name
  k3s_url = each.key == local.first_server_name ? "" : (
    contains(keys(local.hetzner_nodes), local.first_server_name)
    ? "https://${module.hetzner_nodes[local.first_server_name].public_ip}:6443"
    : contains(keys(local.digitalocean_nodes), local.first_server_name)
    ? "https://${module.digitalocean_nodes[local.first_server_name].public_ip}:6443"
    : "https://${module.vultr_nodes[local.first_server_name].public_ip}:6443"
  )
}

# ---------------------------------------------------------------------------
# Hetzner nodes
# ---------------------------------------------------------------------------

module "hetzner_nodes" {
  source   = "./modules/hetzner"
  for_each = local.hetzner_nodes

  name                = each.key
  size                = each.value.size
  region              = each.value.region
  role                = each.value.role
  cloud_init_rendered = module.cloud_init[each.key].cloud_init_rendered
  ssh_key_id          = var.hetzner_ssh_key_id
}

# ---------------------------------------------------------------------------
# DigitalOcean nodes
# ---------------------------------------------------------------------------

module "digitalocean_nodes" {
  source   = "./modules/digitalocean"
  for_each = local.digitalocean_nodes

  name                = each.key
  size                = each.value.size
  region              = each.value.region
  role                = each.value.role
  cloud_init_rendered = module.cloud_init[each.key].cloud_init_rendered
  ssh_key_id          = var.digitalocean_ssh_key_id
}

# ---------------------------------------------------------------------------
# Vultr nodes
# ---------------------------------------------------------------------------

module "vultr_nodes" {
  source   = "./modules/vultr"
  for_each = local.vultr_nodes

  name                = each.key
  size                = each.value.size
  region              = each.value.region
  role                = each.value.role
  cloud_init_rendered = module.cloud_init[each.key].cloud_init_rendered
  ssh_key_id          = var.vultr_ssh_key_id
}
