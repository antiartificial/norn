package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

func init() {
	snapshotsCmd.AddCommand(snapshotsExportCmd)
	snapshotsCmd.AddCommand(snapshotsRemoteCmd)
	snapshotsCmd.AddCommand(snapshotsImportCmd)
}

var snapshotsExportCmd = &cobra.Command{
	Use:   "export <app>",
	Short: "Export latest snapshot to remote storage",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]

		fmt.Printf("%s exporting snapshot for %s...\n", style.DotWarning, appID)
		result, err := client.ExportSnapshot(appID)
		if err != nil {
			return fmt.Errorf("export failed: %w", err)
		}

		fmt.Println(style.SuccessBox.Render("snapshot exported"))
		fmt.Printf("  %s %s\n", style.Key.Render("app"), result.App)
		fmt.Printf("  %s %s\n", style.Key.Render("bucket"), result.Bucket)
		fmt.Printf("  %s %s\n", style.Key.Render("key"), result.Key)
		return nil
	},
}

var snapshotsRemoteCmd = &cobra.Command{
	Use:   "remote <app>",
	Short: "List remote snapshots",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]

		snapshots, err := client.ListRemoteSnapshots(appID)
		if err != nil {
			return fmt.Errorf("failed to list remote snapshots: %w", err)
		}

		if len(snapshots) == 0 {
			fmt.Println(style.DimText.Render("no remote snapshots found"))
			return nil
		}

		fmt.Println(style.Title.Render("remote snapshots for " + appID))
		fmt.Println()

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  "+
			style.TableHeader.Render("KEY")+"\t"+
			style.TableHeader.Render("SIZE")+"\t"+
			style.TableHeader.Render("LAST MODIFIED"))

		for _, s := range snapshots {
			fmt.Fprintf(w, "  %s\t%s\t%s\n",
				s.Key, formatBytes(s.Size), s.LastModified)
		}
		w.Flush()
		return nil
	},
}

var snapshotsImportCmd = &cobra.Command{
	Use:   "import <app> <key>",
	Short: "Import a snapshot from remote storage",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]
		key := args[1]

		fmt.Printf("%s importing snapshot %s for %s...\n", style.DotWarning, key, appID)
		if err := client.ImportSnapshot(appID, key); err != nil {
			return fmt.Errorf("import failed: %w", err)
		}

		fmt.Println(style.SuccessBox.Render("snapshot imported"))
		return nil
	},
}
