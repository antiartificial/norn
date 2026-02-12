package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"norn/cli/api"
	"norn/cli/style"
)

var clusterKubeconfigCmd = &cobra.Command{
	Use:   "kubeconfig",
	Short: "Get cluster kubeconfig",
	RunE:  runClusterKubeconfig,
}

func init() {
	clusterCmd.AddCommand(clusterKubeconfigCmd)
}

func runClusterKubeconfig(cmd *cobra.Command, args []string) error {
	nodes, err := client.ListClusterNodes()
	if err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}

	var serverNode *api.ClusterNode
	for i, n := range nodes {
		if n.Role == "server" && n.Status == "ready" {
			serverNode = &nodes[i]
			break
		}
	}

	if serverNode == nil {
		return fmt.Errorf("no ready server nodes found")
	}

	fmt.Println(style.Banner.Render("KUBECONFIG"))
	fmt.Println()

	tailscaleIP := serverNode.TailscaleIP
	if tailscaleIP == "" {
		tailscaleIP = serverNode.PublicIP
	}

	fmt.Printf("To fetch kubeconfig from %s:\n\n", serverNode.Name)
	fmt.Printf("  ssh root@%s 'cat /etc/rancher/k3s/k3s.yaml' | \\\n", tailscaleIP)
	fmt.Printf("    sed 's/127.0.0.1/%s/' > ~/.kube/norn-config\n\n", tailscaleIP)
	fmt.Printf("  export KUBECONFIG=~/.kube/norn-config\n")
	fmt.Printf("  kubectl get nodes\n")

	return nil
}
