variable "name" {
  description = "Server name"
  type        = string
}

variable "size" {
  description = "Hetzner server type (cx22, cx32, etc.)"
  type        = string
  default     = "cx22"
}

variable "region" {
  description = "Hetzner location (fsn1, nbg1, hel1)"
  type        = string
  default     = "fsn1"
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
  description = "Hetzner SSH key ID"
  type        = string
}
