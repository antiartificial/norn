variable "name_prefix" {
  description = "Unique, lowercase prefix for every disposable lab resource."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{2,23}$", var.name_prefix))
    error_message = "name_prefix must be 3-24 lowercase letters, digits, or hyphens and start with a letter."
  }
}

variable "owner" {
  description = "Operator or team responsible for the billable lab."
  type        = string

  validation {
    condition     = length(trimspace(var.owner)) >= 2
    error_message = "owner is required."
  }
}

variable "expires_on" {
  description = "Required UTC expiry date (YYYY-MM-DD); cleanup tooling refuses an empty value."
  type        = string

  validation {
    condition     = can(regex("^20[0-9]{2}-[0-9]{2}-[0-9]{2}$", var.expires_on))
    error_message = "expires_on must use YYYY-MM-DD."
  }
}

variable "region" {
  description = "DigitalOcean region for the isolated HA lab."
  type        = string
  default     = "tor1"
}

variable "vpc_cidr" {
  description = "Private address range for the lab VPC."
  type        = string
  default     = "10.140.0.0/20"
}

variable "node_count" {
  description = "Odd number of co-located Consul/Nomad/PostgreSQL members. Production must separate failure domains."
  type        = number
  default     = 3

  validation {
    condition     = var.node_count >= 3 && var.node_count % 2 == 1
    error_message = "node_count must be an odd number of at least 3."
  }
}

variable "node_size" {
  description = "Droplet size. s-2vcpu-4gb is the tested minimum for the co-located lab topology."
  type        = string
  default     = "s-2vcpu-4gb"
}

variable "image" {
  description = "Operating-system image slug for convenience. Production-like labs should select an immutable image ID before the first apply; changing it replaces every member."
  type        = string
  default     = "ubuntu-24-04-x64"
}

variable "ssh_key_fingerprints" {
  description = "Existing DigitalOcean SSH key fingerprints or IDs."
  type        = list(string)

  validation {
    condition     = length(var.ssh_key_fingerprints) > 0
    error_message = "At least one SSH key is required."
  }
}

variable "ssh_source_cidrs" {
  description = "Exact public CIDRs allowed to SSH, normally one operator /32."
  type        = list(string)

  validation {
    condition     = length(var.ssh_source_cidrs) > 0 && alltrue([for cidr in var.ssh_source_cidrs : cidr != "0.0.0.0/0"])
    error_message = "Supply at least one bounded SSH source CIDR; 0.0.0.0/0 is forbidden."
  }
}

variable "enable_backups" {
  description = "Enable DigitalOcean whole-droplet backups in addition to database-native recovery tests."
  type        = bool
  default     = false
}

variable "ingress_port" {
  description = "Static host port used by the regional Traefik system job."
  type        = number
  default     = 18080

  validation {
    condition     = var.ingress_port >= 1024 && var.ingress_port <= 65535
    error_message = "ingress_port must be an unprivileged TCP port."
  }
}
