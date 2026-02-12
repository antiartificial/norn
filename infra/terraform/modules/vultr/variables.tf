variable "name" {
  description = "Instance name"
  type        = string
}

variable "size" {
  description = "Vultr plan (vc2-1c-2gb, etc.)"
  type        = string
  default     = "vc2-1c-2gb"
}

variable "region" {
  description = "Vultr region (ewr, lax, etc.)"
  type        = string
  default     = "ewr"
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
  description = "Vultr SSH key ID"
  type        = string
}
