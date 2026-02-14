package cmd

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

func init() {
	rootCmd.AddCommand(statusCmd)
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show status of all apps",
	RunE: func(cmd *cobra.Command, args []string) error {
		apps, err := client.ListApps()
		if err != nil {
			return fmt.Errorf("failed to fetch apps: %w", err)
		}

		if len(apps) == 0 {
			fmt.Println(style.DimText.Render("no apps discovered"))
			return nil
		}

		fmt.Println(style.Title.Render("norn v2 status"))
		fmt.Println()

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, style.TableHeader.Render("APP")+"\t"+
			style.TableHeader.Render("STATUS")+"\t"+
			style.TableHeader.Render("ALLOCS")+"\t"+
			style.TableHeader.Render("PROCESSES"))

		for _, app := range apps {
			name := app.Spec.App
			dot := style.NomadStatusDot(app.NomadStatus)

			allocStr := fmt.Sprintf("%d", len(app.Allocations))
			runningCount := 0
			for _, a := range app.Allocations {
				if a.Status == "running" {
					runningCount++
				}
			}
			if len(app.Allocations) > 0 {
				allocStr = fmt.Sprintf("%d/%d", runningCount, len(app.Allocations))
			}

			var procs []string
			for pName := range app.Spec.Processes {
				procs = append(procs, pName)
			}

			fmt.Fprintf(w, "%s %s\t%s\t%s\t%s\n",
				dot,
				style.Bold.Render(name),
				app.NomadStatus,
				allocStr,
				strings.Join(procs, ", "),
			)
		}
		w.Flush()
		return nil
	},
}
