package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"norn/cli/api"
	"norn/cli/style"
)

// Approximate monthly pricing (USD) for common instance sizes.
// Hetzner prices converted from EUR at ~1.08.
var pricingMap = map[string]map[string]float64{
	"hetzner": {
		"cx22":  3.99,
		"cx32":  7.59,
		"cx42":  14.39,
		"cx52":  28.79,
		"cpx11": 3.99,
		"cpx21": 6.49,
		"cpx31": 11.99,
		"cpx41": 22.49,
		"cpx51": 40.49,
		"cax11": 3.59,
		"cax21": 5.79,
		"cax31": 10.79,
		"cax41": 19.79,
	},
	"digitalocean": {
		"s-1vcpu-512mb-10gb": 4.00,
		"s-1vcpu-1gb":        6.00,
		"s-1vcpu-2gb":        12.00,
		"s-2vcpu-2gb":        18.00,
		"s-2vcpu-4gb":        24.00,
		"s-4vcpu-8gb":        48.00,
	},
	"vultr": {
		"vc2-1c-1gb":  5.00,
		"vc2-1c-2gb":  10.00,
		"vc2-2c-4gb":  20.00,
		"vc2-4c-8gb":  40.00,
		"vc2-6c-16gb": 80.00,
	},
}

func estimateMonthlyCost(provider, size string) string {
	if sizes, ok := pricingMap[provider]; ok {
		if price, ok := sizes[size]; ok {
			return fmt.Sprintf("$%.2f", price)
		}
	}
	return "?"
}

var clusterCostCmd = &cobra.Command{
	Use:   "cost",
	Short: "Show estimated monthly cost of cluster nodes",
	RunE:  runClusterCost,
}

func init() {
	clusterCmd.AddCommand(clusterCostCmd)
}

func runClusterCost(cmd *cobra.Command, args []string) error {
	nodes, err := client.ListClusterNodes()
	if err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}
	if len(nodes) == 0 {
		fmt.Println(style.DimText.Render("No cluster nodes."))
		return nil
	}

	printCostTable(nodes)
	return nil
}

func printCostTable(nodes []api.ClusterNode) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPROVIDER\tSIZE\tSTATUS\t$/MONTH")

	var total float64
	hasUnknown := false

	for _, n := range nodes {
		cost := estimateMonthlyCost(n.Provider, n.Size)
		if cost == "?" {
			hasUnknown = true
		} else {
			if sizes, ok := pricingMap[n.Provider]; ok {
				if price, ok := sizes[n.Size]; ok {
					total += price
				}
			}
		}

		status := n.Status
		switch status {
		case "ready":
			status = style.StepDone.Render(status)
		case "failed":
			status = style.StepFailed.Render(status)
		default:
			status = style.DimText.Render(status)
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", n.Name, n.Provider, n.Size, status, cost)
	}

	fmt.Fprintf(w, "\t\t\t\t────────\n")
	fmt.Fprintf(w, "\t\t\tTotal\t$%.2f/mo\n", total)
	w.Flush()

	if hasUnknown {
		fmt.Println(style.DimText.Render("\n  ? = unknown size; price not in catalog"))
	}
}
