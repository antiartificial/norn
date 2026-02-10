package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"norn/cli/style"
)

var healthCmd = &cobra.Command{
	Use:     "health",
	Short:   "Check health of all backing services",
	Aliases: []string{"doctor", "h"},
	RunE:    runHealth,
}

func init() {
	rootCmd.AddCommand(healthCmd)
}

func runHealth(cmd *cobra.Command, args []string) error {
	h, err := client.Health()
	if err != nil {
		fmt.Println(style.ErrorBox.Render("Cannot reach Norn API at " + apiURL))
		return err
	}

	fmt.Println(style.Banner.Render("⚡ NORN HEALTH"))
	fmt.Println()

	serviceNames := map[string]string{
		"postgres":   "PostgreSQL",
		"kubernetes": "Kubernetes",
		"valkey":     "Valkey",
		"redpanda":   "Redpanda",
		"sops":       "SOPS",
	}

	order := []string{"postgres", "kubernetes", "valkey", "redpanda", "sops"}
	allUp := true

	for _, key := range order {
		status := h.Services[key]
		name := serviceNames[key]
		if name == "" {
			name = key
		}

		dot := style.ServiceDot(status)
		label := style.DimText.Render(status)
		switch status {
		case "up":
			label = style.Healthy.Render("up")
		case "down":
			label = style.Unhealthy.Render("down")
			allUp = false
		default:
			label = style.Warning.Render(status)
		}

		fmt.Printf("  %s  %-14s %s\n", dot, style.Bold.Render(name), label)
	}

	fmt.Println()

	if allUp {
		fmt.Println(style.SuccessBox.Render("All services healthy"))
	} else {
		fmt.Println(style.ErrorBox.Render("Some services are down — run `make doctor` for troubleshooting hints"))
	}

	return nil
}
