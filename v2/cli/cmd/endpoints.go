package cmd

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

func init() {
	rootCmd.AddCommand(endpointsCmd)
	endpointsCmd.AddCommand(endpointsToggleCmd)
}

var endpointsCmd = &cobra.Command{
	Use:   "endpoints <app>",
	Short: "List endpoints and their cloudflared status",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]

		app, err := client.GetApp(appID)
		if err != nil {
			return fmt.Errorf("failed to fetch app: %w", err)
		}

		if len(app.Spec.Endpoints) == 0 {
			fmt.Println(style.DimText.Render("no endpoints configured for " + appID))
			return nil
		}

		// Fetch active ingress entries (may fail in dev mode)
		activeIngress, err := client.CloudflaredIngress()
		ingressAvailable := err == nil

		fmt.Println(style.Title.Render("endpoints for " + appID))
		fmt.Println()

		activeSet := map[string]bool{}
		for _, h := range activeIngress {
			activeSet[h] = true
		}

		for _, ep := range app.Spec.Endpoints {
			displayName := extractHostname(ep.URL)
			active := activeSet[displayName]

			if !ingressAvailable {
				fmt.Printf("  %s %-36s %s\n",
					style.DimText.Render("?"),
					displayName,
					style.DimText.Render("unknown"),
				)
			} else if active {
				fmt.Printf("  %s %-36s %s\n",
					style.Healthy.Render("●"),
					displayName,
					style.Healthy.Render("active"),
				)
			} else {
				fmt.Printf("  %s %-36s %s\n",
					style.DimText.Render("○"),
					displayName,
					style.DimText.Render("inactive"),
				)
			}
		}

		return nil
	},
}

var endpointsToggleCmd = &cobra.Command{
	Use:   "toggle <app> <hostname>",
	Short: "Toggle a single endpoint on or off in cloudflared",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]
		hostname := args[1]

		// Determine current state from ingress list
		activeIngress, err := client.CloudflaredIngress()
		if err != nil {
			return fmt.Errorf("cannot read cloudflared ingress: %w", err)
		}

		currentlyActive := false
		for _, h := range activeIngress {
			if h == hostname {
				currentlyActive = true
				break
			}
		}

		newState := !currentlyActive
		action := "disabled"
		if newState {
			action = "enabled"
		}

		fmt.Printf("%s %s\n\n",
			style.Title.Render("toggling "+hostname+" →"),
			style.Bold.Render(action),
		)

		if err := client.ToggleEndpoint(appID, hostname, newState); err != nil {
			return fmt.Errorf("toggle failed: %w", err)
		}

		fmt.Println(style.SuccessBox.Render("cloudflared updated"))
		return nil
	},
}

func extractHostname(rawURL string) string {
	if !strings.Contains(rawURL, "://") {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return u.Hostname()
}
