# ---------------------------------------------------------------------------
# Cluster secrets
# ---------------------------------------------------------------------------

variable "k3s_token" {
  description = "Shared secret for k3s cluster join"
  type        = string
  sensitive   = true
}

variable "tailscale_auth_key" {
  description = "Tailscale auth key for node enrollment"
  type        = string
  sensitive   = true
}

# ---------------------------------------------------------------------------
# Provider credentials
# ---------------------------------------------------------------------------

variable "hcloud_token" {
  description = "Hetzner Cloud API token"
  type        = string
  sensitive   = true
}

variable "do_token" {
  description = "DigitalOcean API token"
  type        = string
  sensitive   = true
}

variable "vultr_api_key" {
  description = "Vultr API key"
  type        = string
  sensitive   = true
}

# ---------------------------------------------------------------------------
# SSH keys (per-provider)
# ---------------------------------------------------------------------------

variable "hetzner_ssh_key_id" {
  description = "Hetzner SSH key ID (from hcloud ssh-key list)"
  type        = string
}

variable "digitalocean_ssh_key_id" {
  description = "DigitalOcean SSH key ID or fingerprint"
  type        = string
}

variable "vultr_ssh_key_id" {
  description = "Vultr SSH key ID"
  type        = string
}

variable "ssh_public_key" {
  description = "SSH public key content (for bootstrapping if needed)"
  type        = string
  default     = ""
}

# ---------------------------------------------------------------------------
# Node definitions
# ---------------------------------------------------------------------------

variable "nodes" {
  description = "Map of node definitions keyed by node name"
  type = map(object({
    provider = string # hetzner, digitalocean, vultr
    region   = string
    size     = string
    role     = string # server, agent
  }))

  validation {
    condition     = alltrue([for v in values(var.nodes) : contains(["hetzner", "digitalocean", "vultr"], v.provider)])
    error_message = "Each node's provider must be one of: hetzner, digitalocean, vultr."
  }

  validation {
    condition     = alltrue([for v in values(var.nodes) : contains(["server", "agent"], v.role)])
    error_message = "Each node's role must be 'server' or 'agent'."
  }

  validation {
    condition     = length([for v in values(var.nodes) : v if v.role == "server"]) >= 1
    error_message = "At least one server node is required."
  }
}
