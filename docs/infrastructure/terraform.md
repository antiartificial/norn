# Terraform

Norn uses Terraform to provision Kubernetes clusters across multiple cloud providers. The infrastructure code supports Hetzner, DigitalOcean, and Vultr.

## Directory Structure

```
infra/terraform/
├── main.tf
├── variables.tf
├── outputs.tf
└── modules/
    ├── hetzner/
    ├── digitalocean/
    ├── vultr/
    └── k3s-node/
```

- `main.tf`: Root module that orchestrates cluster provisioning
- `variables.tf`: Input variables for configuration
- `outputs.tf`: Exported values (node IPs, cluster endpoints)
- `modules/`: Reusable components for each cloud provider and k3s node configuration

## Modules

### Cloud Provider Modules

- `hetzner`: Hetzner Cloud resources (servers, networks)
- `digitalocean`: DigitalOcean resources (droplets, VPCs)
- `vultr`: Vultr resources (instances)

### k3s-node Module

Generates cloud-init templates that:

- Install k3s on the node
- Configure server or agent role
- Enroll the node in Tailscale for mesh networking

## Configuration

The `nodes` variable defines the cluster topology as a map of objects:

```hcl
nodes = {
  "node-1" = {
    provider = "hetzner"
    region   = "nbg1"
    size     = "cx11"
    role     = "server"
  }
  "node-2" = {
    provider = "digitalocean"
    region   = "nyc3"
    size     = "s-1vcpu-1gb"
    role     = "agent"
  }
}
```

### Validation Rules

- `provider`: Must be `hetzner`, `digitalocean`, or `vultr`
- `role`: Must be `server` or `agent`
- At least one `server` node is required

## Provider Credentials

All provider tokens are marked sensitive:

- `hcloud_token`: Hetzner Cloud API token
- `do_token`: DigitalOcean API token
- `vultr_api_key`: Vultr API key

SSH key IDs must be configured for each provider to enable access to provisioned nodes.

## Cluster Initialization

The first server node initializes the k3s cluster. Additional server nodes join in HA mode. Agent nodes join via the server's API endpoint.

### Variables

- `k3s_token`: Shared secret for cluster join authentication
- `tailscale_auth_key`: Auth key for enrolling nodes in Tailscale mesh

Both values are sensitive and should be managed via environment variables or a `.tfvars` file excluded from version control.

## Cloud-Init

The `k3s-node` module generates cloud-init user data that:

1. Installs k3s as server or agent
2. Configures cluster join parameters (token, server URL)
3. Enrolls the node in Tailscale for private networking

Server nodes use the k3s installation script with `--cluster-init` for the first server, or `--server` with the existing server URL for additional servers. Agent nodes use `--agent` and connect to the server API.

## CLI Integration

The Norn CLI provides a command to initialize the cluster:

```bash
norn cluster init
```

This command:

1. Runs `terraform init` to download provider plugins
2. Runs `terraform apply` with the configured variables
3. Waits for cluster readiness

## Outputs

Terraform outputs include:

- Node IP addresses (public and private)
- Cluster API endpoint
- SSH connection details

These outputs are used by the CLI and API to interact with the provisioned cluster.
