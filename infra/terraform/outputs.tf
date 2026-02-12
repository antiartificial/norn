# ---------------------------------------------------------------------------
# Node IPs (merged from all providers)
# ---------------------------------------------------------------------------

output "node_ips" {
  description = "Public IPs of all nodes, keyed by node name"
  value = merge(
    { for k, v in module.hetzner_nodes : k => v.public_ip },
    { for k, v in module.digitalocean_nodes : k => v.public_ip },
    { for k, v in module.vultr_nodes : k => v.public_ip },
  )
}

output "hetzner_node_ips" {
  description = "Public IPs of Hetzner nodes"
  value       = { for k, v in module.hetzner_nodes : k => v.public_ip }
}

output "digitalocean_node_ips" {
  description = "Public IPs of DigitalOcean nodes"
  value       = { for k, v in module.digitalocean_nodes : k => v.public_ip }
}

output "vultr_node_ips" {
  description = "Public IPs of Vultr nodes"
  value       = { for k, v in module.vultr_nodes : k => v.public_ip }
}

output "kubeconfig_hint" {
  description = "How to retrieve kubeconfig from the first server"
  value       = "ssh root@<first-server-ip> cat /etc/rancher/k3s/k3s.yaml"
}
