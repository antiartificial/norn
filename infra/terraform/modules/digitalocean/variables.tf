variable "name" {
  description = "Droplet name"
  type        = string
}

variable "size" {
  description = "DigitalOcean droplet size (s-1vcpu-2gb, etc.)"
  type        = string
  default     = "s-1vcpu-2gb"
}

variable "region" {
  description = "DigitalOcean region (nyc1, sfo3, etc.)"
  type        = string
  default     = "nyc1"
}

variable "role" {
  description = "Node role: server or agent"
  type        = string
}

variable "cloud_init_rendered" {
  description = "Rendered cloud-init user data"
  type        = string
  sensitive   = true
}

variable "ssh_key_id" {
  description = "DigitalOcean SSH key ID or fingerprint"
  type        = string
}
