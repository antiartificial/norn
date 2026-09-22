locals {
  common_tags = [
    "norn",
    "norn-ha-lab",
    "owner-${replace(lower(var.owner), "_", "-")}",
    "expires-${var.expires_on}",
  ]
  nodes = {
    for index in range(var.node_count) : format("%s-%02d", var.name_prefix, index + 1) => index
  }
}

data "digitalocean_sizes" "selected" {
  filter {
    key    = "slug"
    values = [var.node_size]
  }
}

resource "digitalocean_vpc" "lab" {
  name     = "${var.name_prefix}-vpc"
  region   = var.region
  ip_range = var.vpc_cidr
}

resource "digitalocean_droplet" "node" {
  for_each = local.nodes

  name       = each.key
  region     = var.region
  size       = var.node_size
  image      = var.image
  vpc_uuid   = digitalocean_vpc.lab.id
  ssh_keys   = var.ssh_key_fingerprints
  monitoring = true
  backups    = var.enable_backups
  ipv6       = true
  tags       = concat(local.common_tags, ["norn-ha-member"])

  user_data = templatefile("${path.module}/cloud-init.yaml.tftpl", {
    hostname          = each.key
    tailscale_authkey = var.tailscale_authkey
  })

  lifecycle {
    precondition {
      condition     = var.node_count >= 3
      error_message = "An HA evaluation cannot be created with fewer than three members."
    }
    precondition {
      condition     = try(length(data.digitalocean_sizes.selected.sizes) == 1 && data.digitalocean_sizes.selected.sizes[0].available && contains(data.digitalocean_sizes.selected.sizes[0].regions, var.region), false)
      error_message = "The selected Droplet size must exist, be available, and support the selected region."
    }
  }
}

resource "digitalocean_loadbalancer" "regional_ingress" {
  name        = "${var.name_prefix}-ingress"
  region      = var.region
  type        = "REGIONAL"
  network     = "EXTERNAL"
  size        = "lb-small"
  vpc_uuid    = digitalocean_vpc.lab.id
  droplet_ids = [for node in digitalocean_droplet.node : node.id]

  forwarding_rule {
    entry_protocol  = "http"
    entry_port      = 80
    target_protocol = "http"
    target_port     = var.ingress_port
  }

  healthcheck {
    protocol                 = "http"
    port                     = var.ingress_port
    path                     = "/ping"
    check_interval_seconds   = 10
    response_timeout_seconds = 5
    healthy_threshold        = 2
    unhealthy_threshold      = 3
  }
}

resource "digitalocean_firewall" "lab" {
  name        = "${var.name_prefix}-firewall"
  droplet_ids = [for node in digitalocean_droplet.node : node.id]

  inbound_rule {
    protocol         = "tcp"
    port_range       = "22"
    source_addresses = var.ssh_source_cidrs
  }

  inbound_rule {
    protocol                  = "tcp"
    port_range                = tostring(var.ingress_port)
    source_load_balancer_uids = [digitalocean_loadbalancer.regional_ingress.id]
  }

  inbound_rule {
    protocol         = "udp"
    port_range       = "41641"
    source_addresses = ["0.0.0.0/0", "::/0"]
  }

  # Cluster traffic is private-VPC-only. The broad port range keeps the lab
  # compatible with scheduler dynamic ports without exposing them publicly.
  inbound_rule {
    protocol         = "tcp"
    port_range       = "1-65535"
    source_addresses = [var.vpc_cidr]
  }

  inbound_rule {
    protocol         = "udp"
    port_range       = "1-65535"
    source_addresses = [var.vpc_cidr]
  }

  inbound_rule {
    protocol         = "icmp"
    source_addresses = [var.vpc_cidr]
  }

  outbound_rule {
    protocol              = "tcp"
    port_range            = "1-65535"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }

  outbound_rule {
    protocol              = "udp"
    port_range            = "1-65535"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }

  outbound_rule {
    protocol              = "icmp"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }
}

# Optional DigitalOcean managed PostgreSQL. Opt-in via use_managed_db; when disabled (default) the
# fleet keeps its co-located self-managed Patroni database and none of these resources are created.
# When enabled, the app's DATABASE_URL is pointed here (see scripts/catalog + outputs.managed_db_uri).
resource "digitalocean_database_cluster" "pg" {
  count = var.use_managed_db ? 1 : 0

  name                 = "${var.name_prefix}-pg"
  engine               = "pg"
  version              = var.db_version
  size                 = var.db_size
  region               = var.region
  node_count           = 1
  private_network_uuid = digitalocean_vpc.lab.id
  tags                 = local.common_tags
}

resource "digitalocean_database_db" "app" {
  count      = var.use_managed_db ? 1 : 0
  cluster_id = digitalocean_database_cluster.pg[0].id
  name       = "norn_test"
}

resource "digitalocean_database_user" "app" {
  count      = var.use_managed_db ? 1 : 0
  cluster_id = digitalocean_database_cluster.pg[0].id
  name       = "norn_test"
}

# Trust only the fleet droplets. Combined with private_network_uuid this keeps the managed database
# reachable exclusively from inside the lab VPC.
resource "digitalocean_database_firewall" "pg" {
  count      = var.use_managed_db ? 1 : 0
  cluster_id = digitalocean_database_cluster.pg[0].id

  dynamic "rule" {
    for_each = digitalocean_droplet.node
    content {
      type  = "droplet"
      value = rule.value.id
    }
  }
}

resource "digitalocean_project" "lab" {
  name        = "${var.name_prefix} (expires ${var.expires_on})"
  description = "Disposable Norn HA/PITR acceptance lab owned by ${var.owner}."
  purpose     = "Operational / Developer tooling"
  environment = "Development"
  resources = concat(
    [for node in digitalocean_droplet.node : node.urn],
    [digitalocean_loadbalancer.regional_ingress.urn],
    var.use_managed_db ? [digitalocean_database_cluster.pg[0].urn] : [],
  )
}
