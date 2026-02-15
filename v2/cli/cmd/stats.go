package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

func init() {
	rootCmd.AddCommand(statsCmd)
}

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show deployment and cluster statistics",
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := client.Stats()
		if err != nil {
			return fmt.Errorf("failed to fetch stats: %w", err)
		}

		fmt.Println(style.Title.Render("norn stats"))
		fmt.Println()

		fmt.Printf("  %s %d\n", style.Key.Render("apps"), s.AppCount)
		fmt.Printf("  %s %d/%d\n", style.Key.Render("allocations"), s.RunningAllocs, s.TotalAllocs)
		fmt.Println()

		fmt.Println(style.Bold.Render("  today's deploys"))
		fmt.Printf("    total: %d  success: %d  failed: %d\n",
			s.Deploys.Total, s.Deploys.Success, s.Deploys.Failed)
		if s.Deploys.MostPopularApp != "" {
			fmt.Printf("    most deployed: %s (%dx)\n", s.Deploys.MostPopularApp, s.Deploys.MostPopularN)
		}
		fmt.Println()

		if len(s.UptimeLeaderboard) > 0 {
			fmt.Println(style.Bold.Render("  uptime leaderboard"))
			fmt.Println()

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "    "+
				style.TableHeader.Render("JOB")+"\t"+
				style.TableHeader.Render("GROUP")+"\t"+
				style.TableHeader.Render("UPTIME")+"\t"+
				style.TableHeader.Render("NODE"))

			for _, e := range s.UptimeLeaderboard {
				fmt.Fprintf(w, "    %s\t%s\t%s\t%s\n",
					e.JobID, e.TaskGroup, e.Uptime, e.NodeName)
			}
			w.Flush()
		}

		return nil
	},
}
