output "public_ip" {
  description = "Public IPv4 address of the droplet"
  value       = digitalocean_droplet.node.ipv4_address
}

output "id" {
  description = "DigitalOcean droplet ID"
  value       = digitalocean_droplet.node.id
}

output "name" {
  description = "Droplet name"
  value       = digitalocean_droplet.node.name
}
