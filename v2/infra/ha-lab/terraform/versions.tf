terraform {
  required_version = ">= 1.8.0"

  # Configuration is supplied by scripts/state-backend so credentials remain
  # in the controller environment instead of Terraform files or state.
  backend "s3" {}

  required_providers {
    digitalocean = {
      source  = "digitalocean/digitalocean"
      version = "~> 2.0"
    }
  }
}

provider "digitalocean" {}
