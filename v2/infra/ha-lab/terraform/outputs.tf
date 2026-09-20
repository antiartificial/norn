output "lab_nodes" {
  description = "Machine-readable input for the generated Ansible inventory."
  value = {
    for name, node in digitalocean_droplet.node : name => {
      id         = node.id
      public_ip  = node.ipv4_address
      private_ip = node.ipv4_address_private
      region     = node.region
    }
  }
}

output "estimated_monthly_droplet_cost_usd" {
  description = "Informational estimate for the default three s-2vcpu-4gb nodes; verify current pricing before apply."
  value       = var.node_count * 24
}

output "expiry" {
  value = {
    owner      = var.owner
    expires_on = var.expires_on
  }
}

output "name_prefix" {
  value = var.name_prefix
}

output "vpc_cidr" {
  value = var.vpc_cidr
}

output "regional_ingress" {
  description = "Regional DigitalOcean load balancer fronting Traefik on every member."
  value = {
    id   = digitalocean_loadbalancer.regional_ingress.id
    ip   = digitalocean_loadbalancer.regional_ingress.ip
    port = var.ingress_port
  }
}

output "use_managed_db" {
  description = "Whether the fleet uses a DigitalOcean managed database instead of co-located Patroni."
  value       = var.use_managed_db
}

output "managed_db_uri" {
  description = "Private DSN for the optional managed PostgreSQL. Empty string when self-managed."
  sensitive   = true
  value = var.use_managed_db ? format(
    "postgres://%s:%s@%s:%d/%s?sslmode=require",
    digitalocean_database_user.app[0].name,
    digitalocean_database_user.app[0].password,
    digitalocean_database_cluster.pg[0].private_host,
    digitalocean_database_cluster.pg[0].port,
    digitalocean_database_db.app[0].name,
  ) : ""
}
