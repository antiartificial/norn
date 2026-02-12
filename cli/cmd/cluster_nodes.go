package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"norn/cli/style"
)

var clusterNodesCmd = &cobra.Command{
	Use:   "nodes",
	Short: "List cluster nodes",
	RunE:  runClusterNodes,
}

func init() {
	clusterCmd.AddCommand(clusterNodesCmd)
}

func runClusterNodes(cmd *cobra.Command, args []string) error {
	nodes, err := client.ListClusterNodes()
	if err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}
	if len(nodes) == 0 {
		fmt.Println(style.DimText.Render("No cluster nodes. Run 'norn cluster init' to get started."))
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPROVIDER\tREGION\tROLE\tSTATUS\tIP\tTAILSCALE IP")
	for _, n := range nodes {
		status := n.Status
		switch status {
		case "ready":
			status = style.StepDone.Render(status)
		case "failed":
			status = style.StepFailed.Render(status)
		default:
			status = style.DimText.Render(status)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			n.Name, n.Provider, n.Region, n.Role, status, n.PublicIP, n.TailscaleIP)
	}
	w.Flush()
	return nil
}
