#cloud-config

package_update: true
package_upgrade: true

packages:
  - curl
  - jq

write_files:
  - path: /usr/local/bin/setup-k3s.sh
    permissions: "0755"
    content: |
      #!/bin/bash
      set -euo pipefail

      ROLE="${role}"
      NODE_NAME="${node_name}"
      K3S_TOKEN="${k3s_token}"
      K3S_URL="${k3s_url}"
      TAILSCALE_AUTH_KEY="${tailscale_auth_key}"
      IS_FIRST_SERVER="${is_first_server}"

      # --- Install Tailscale ---
      curl -fsSL https://tailscale.com/install.sh | sh
      tailscale up --authkey="$TAILSCALE_AUTH_KEY" --hostname="$NODE_NAME"

      # --- Wait for Tailscale interface ---
      echo "Waiting for Tailscale interface..."
      for i in $(seq 1 60); do
        TAILSCALE_IP=$(tailscale ip -4 2>/dev/null || true)
        if [ -n "$TAILSCALE_IP" ]; then
          echo "Tailscale IP: $TAILSCALE_IP"
          break
        fi
        sleep 2
      done

      if [ -z "$TAILSCALE_IP" ]; then
        echo "ERROR: Tailscale interface did not come up"
        exit 1
      fi

      # --- Wait for tailscale0 interface ---
      for i in $(seq 1 30); do
        if ip link show tailscale0 &>/dev/null; then
          echo "tailscale0 interface is up"
          break
        fi
        sleep 2
      done

      # --- Get public IP ---
      PUBLIC_IP=$(curl -s https://ifconfig.me)

      # --- Install k3s ---
      export INSTALL_K3S_CHANNEL="stable"

      if [ "$ROLE" = "server" ]; then
        if [ "$IS_FIRST_SERVER" = "true" ]; then
          # First server: initialize the cluster
          curl -sfL https://get.k3s.io | K3S_TOKEN="$K3S_TOKEN" sh -s - server \
            --cluster-init \
            --tls-san "$PUBLIC_IP" \
            --tls-san "$TAILSCALE_IP" \
            --node-ip "$TAILSCALE_IP" \
            --flannel-iface tailscale0 \
            --disable traefik \
            --node-name "$NODE_NAME"
        else
          # Joining server: connect to existing cluster
          curl -sfL https://get.k3s.io | K3S_TOKEN="$K3S_TOKEN" sh -s - server \
            --server "$K3S_URL" \
            --tls-san "$PUBLIC_IP" \
            --tls-san "$TAILSCALE_IP" \
            --node-ip "$TAILSCALE_IP" \
            --flannel-iface tailscale0 \
            --disable traefik \
            --node-name "$NODE_NAME"
        fi
      else
        # Agent: join the cluster
        curl -sfL https://get.k3s.io | K3S_TOKEN="$K3S_TOKEN" K3S_URL="$K3S_URL" sh -s - agent \
          --node-ip "$TAILSCALE_IP" \
          --flannel-iface tailscale0 \
          --node-name "$NODE_NAME"
      fi

runcmd:
  - /usr/local/bin/setup-k3s.sh
