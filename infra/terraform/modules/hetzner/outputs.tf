output "public_ip" {
  description = "Public IPv4 address of the server"
  value       = hcloud_server.node.ipv4_address
}

output "id" {
  description = "Hetzner server ID"
  value       = hcloud_server.node.id
}

output "name" {
  description = "Server name"
  value       = hcloud_server.node.name
}
