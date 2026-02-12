variable "role" {
  description = "Node role: server or agent"
  type        = string

  validation {
    condition     = contains(["server", "agent"], var.role)
    error_message = "Role must be 'server' or 'agent'."
  }
}

variable "k3s_token" {
  description = "Shared secret for k3s cluster join"
  type        = string
  sensitive   = true
}

variable "k3s_url" {
  description = "URL of the k3s server to join (empty for first server)"
  type        = string
  default     = ""
}

variable "node_name" {
  description = "Name for this node"
  type        = string
}

variable "tailscale_auth_key" {
  description = "Tailscale auth key for node enrollment"
  type        = string
  sensitive   = true
}

variable "is_first_server" {
  description = "Whether this is the first server node (cluster-init)"
  type        = bool
  default     = false
}
