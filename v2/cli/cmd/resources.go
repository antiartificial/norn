package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"norn/v2/cli/api"
	"norn/v2/cli/style"
)

func init() {
	rootCmd.AddCommand(resourcesCmd)
}

var resourcesCmd = &cobra.Command{
	Use:   "resources",
	Short: "Show resource usage and right-sizing suggestions",
	RunE: func(cmd *cobra.Command, args []string) error {
		suggestions, err := client.ResourceSuggestions()
		if err != nil {
			return fmt.Errorf("failed to fetch resource suggestions: %w", err)
		}

		if len(suggestions) == 0 {
			fmt.Println("  no running allocations with resource data")
			return nil
		}

		fmt.Println(style.Title.Render("resource suggestions"))
		fmt.Println()

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  "+
			style.TableHeader.Render("APP")+"\t"+
			style.TableHeader.Render("PROCESS")+"\t"+
			style.TableHeader.Render("MEMORY")+"\t"+
			style.TableHeader.Render("USED")+"\t"+
			style.TableHeader.Render("PEAK")+"\t"+
			style.TableHeader.Render("STATUS"))

		for _, s := range suggestions {
			statusStr := s.Status
			switch s.Status {
			case "at_risk":
				statusStr = style.Unhealthy.Render("at risk")
			case "overprovisioned":
				statusStr = style.Warning.Render("overprovisioned")
			case "right_sized":
				statusStr = style.Healthy.Render("right sized")
			default:
				statusStr = style.DimText.Render(s.Status)
			}

			fmt.Fprintf(w, "  %s\t%s\t%d MB\t%d MB\t%d MB\t%s\n",
				s.App, s.Process, s.DeclaredMemMB, s.UsedMemMB, s.PeakMemMB, statusStr)
		}
		w.Flush()

		if hasStatus(suggestions, "at_risk") {
			fmt.Println()
			fmt.Println(style.Warning.Render("  ⚠ apps at risk may OOM — consider increasing memory limits"))
		}
		if hasStatus(suggestions, "overprovisioned") {
			fmt.Println()
			fmt.Println(style.DimText.Render("  overprovisioned apps could have memory limits reduced"))
		}

		return nil
	},
}

func hasStatus(suggestions []api.ResourceSuggestion, status string) bool {
	for _, s := range suggestions {
		if s.Status == status {
			return true
		}
	}
	return false
}
