package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

func init() {
	rootCmd.AddCommand(snapshotsCmd)
}

var snapshotsCmd = &cobra.Command{
	Use:   "snapshots <app> [restore <timestamp>]",
	Short: "List or restore database snapshots",
	Args:  cobra.RangeArgs(1, 3),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]

		if len(args) >= 3 && args[1] == "restore" {
			ts := args[2]
			fmt.Printf("%s restoring snapshot %s for %s...\n", style.DotWarning, ts, appID)
			if err := client.RestoreSnapshot(appID, ts); err != nil {
				return fmt.Errorf("restore failed: %w", err)
			}
			fmt.Println(style.SuccessBox.Render("snapshot restored"))
			return nil
		}

		snaps, err := client.ListSnapshots(appID)
		if err != nil {
			return fmt.Errorf("failed to list snapshots: %w", err)
		}

		if len(snaps) == 0 {
			fmt.Println(style.DimText.Render("no snapshots found"))
			return nil
		}

		fmt.Println(style.Title.Render("snapshots for " + appID))
		fmt.Println()

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  "+
			style.TableHeader.Render("TIMESTAMP")+"\t"+
			style.TableHeader.Render("DATABASE")+"\t"+
			style.TableHeader.Render("SIZE")+"\t"+
			style.TableHeader.Render("FILE"))

		for _, s := range snaps {
			size := formatBytes(s.Size)
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n",
				s.Timestamp, s.Database, size, s.Filename)
		}
		w.Flush()

		return nil
	},
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
