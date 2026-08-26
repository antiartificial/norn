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
  description = "Live provider price for the selected Droplet size multiplied by node count; excludes the load balancer and network usage."
  value       = var.node_count * data.digitalocean_sizes.selected.sizes[0].price_monthly
}

output "estimated_hourly_droplet_cost_usd" {
  description = "Live provider hourly price for the selected Droplet size multiplied by node count; excludes the load balancer and network usage."
  value       = var.node_count * data.digitalocean_sizes.selected.sizes[0].price_hourly
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
