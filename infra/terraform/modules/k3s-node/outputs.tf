output "cloud_init_rendered" {
  description = "Rendered cloud-init configuration"
  value = templatefile("${path.module}/cloud-init.yaml.tpl", {
    role               = var.role
    k3s_token          = var.k3s_token
    k3s_url            = var.k3s_url
    node_name          = var.node_name
    tailscale_auth_key = var.tailscale_auth_key
    is_first_server    = var.is_first_server
  })
  sensitive = true
}
