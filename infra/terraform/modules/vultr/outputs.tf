output "public_ip" {
  description = "Public IPv4 address of the instance"
  value       = vultr_instance.node.main_ip
}

output "id" {
  description = "Vultr instance ID"
  value       = vultr_instance.node.id
}

output "name" {
  description = "Instance name"
  value       = vultr_instance.node.label
}
