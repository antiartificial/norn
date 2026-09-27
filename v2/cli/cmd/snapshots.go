package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"norn/v2/cli/api"
	"norn/v2/cli/style"
)

func init() {
	snapshotsCmd.Flags().BoolVar(&snapshotRestoreYes, "yes", false, "Confirm snapshot restore")
	snapshotsCmd.Flags().BoolVar(&snapshotPreRestore, "pre-restore", false, "Compatibility flag; restores always create a safety snapshot")
	snapshotsCmd.Flags().IntVar(&snapshotRetentionKeep, "keep", 3, "Number of newest snapshots to keep in retention preview")
	snapshotsCmd.Flags().BoolVar(&snapshotRetentionExecute, "execute", false, "Apply snapshot retention pruning")
	snapshotsCmd.Flags().StringVar(&snapshotDatabase, "database", "", "Named database to restore or prune")
	snapshotsCmd.Flags().StringVar(&snapshotIdempotencyKey, "idempotency-key", "", "Stable retry key (generated and printed when omitted)")
	snapshotsCmd.Flags().BoolVar(&snapshotWait, "wait", true, "Wait for a queued restore or pruning operation")
	snapshotsCmd.Flags().DurationVar(&snapshotTimeout, "timeout", 2*time.Hour, "Maximum time to wait for an operation")
	rootCmd.AddCommand(snapshotsCmd)
}

var snapshotRestoreYes bool
var snapshotPreRestore bool
var snapshotRetentionKeep int
var snapshotRetentionExecute bool
var snapshotIdempotencyKey string
var snapshotWait bool
var snapshotTimeout time.Duration
var snapshotDatabase string

var snapshotsCmd = &cobra.Command{
	Use:   "snapshots <app> [restore <timestamp>|retention]",
	Short: "List, restore, or preview database snapshot retention",
	Args:  cobra.RangeArgs(1, 3),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]

		if len(args) >= 3 && args[1] == "restore" {
			ts := args[2]
			if !snapshotRestoreYes {
				return fmt.Errorf("restore is destructive; rerun with --yes to confirm")
			}
			key, err := requestIdempotencyKey(cmd, snapshotIdempotencyKey, "norn-snapshot-restore")
			if err != nil {
				return err
			}
			op, err := client.QueueSnapshotRestore(appID, ts, key, snapshotDatabase)
			if err != nil {
				return fmt.Errorf("queue restore: %w", err)
			}
			return handleSnapshotOperation(cmd, op)
		}

		if len(args) >= 2 && args[1] == "retention" {
			if snapshotRetentionExecute && !snapshotRestoreYes {
				return fmt.Errorf("retention execution deletes old snapshots; rerun with --execute --yes to confirm")
			}
			if snapshotRetentionExecute {
				key, err := requestIdempotencyKey(cmd, snapshotIdempotencyKey, "norn-snapshot-prune")
				if err != nil {
					return err
				}
				op, err := client.QueueSnapshotPrune(appID, snapshotRetentionKeep, key, snapshotDatabase)
				if err != nil {
					return fmt.Errorf("queue snapshot pruning: %w", err)
				}
				return handleSnapshotOperation(cmd, op)
			}
			receipt, err := client.ApplySnapshotRetention(appID, snapshotRetentionKeep, false, snapshotDatabase)
			if err != nil {
				return fmt.Errorf("snapshot retention preview failed: %w", err)
			}
			printSnapshotRetentionReceipt(receipt)
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
			style.TableHeader.Render("CREATED")+"\t"+
			style.TableHeader.Render("COMMIT")+"\t"+
			style.TableHeader.Render("DATABASE")+"\t"+
			style.TableHeader.Render("SIZE")+"\t"+
			style.TableHeader.Render("FILE"))

		for _, s := range snaps {
			size := formatBytes(s.Size)
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n",
				s.Timestamp, s.CreatedAt, s.CommitSHA, s.Database, size, s.Filename)
		}
		w.Flush()

		return nil
	},
}

func handleSnapshotOperation(cmd *cobra.Command, op *api.Operation) error {
	if op == nil || op.ID == "" {
		return fmt.Errorf("snapshot operation acceptance returned no operation id")
	}
	fmt.Printf("%s %s (%s)\n", style.Healthy.Render(op.Status), op.ID, op.Kind)
	if !snapshotWait {
		fmt.Printf("Check status with: norn operations %s\n", op.ID)
		return nil
	}
	deadline := time.Now().Add(snapshotTimeout)
	for {
		switch op.Status {
		case "succeeded":
			fmt.Println(style.SuccessBox.Render(op.Message))
			return nil
		case "failed", "canceled":
			return fmt.Errorf("%s: %s (operation %s)", op.Status, op.Message, op.ID)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for snapshot operation %s", op.ID)
		}
		select {
		case <-cmd.Context().Done():
			return cmd.Context().Err()
		case <-time.After(2 * time.Second):
		}
		updated, err := client.GetOperation(op.ID)
		if err != nil {
			return fmt.Errorf("operation %s continues; status lookup failed: %w", op.ID, err)
		}
		op = updated
	}
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

func printSnapshotRetentionReceipt(receipt *api.SnapshotRetentionReceipt) {
	fmt.Println(style.Title.Render("snapshot retention for " + receipt.App))
	action := "preview-only"
	if !receipt.DryRun {
		action = "applied"
	}
	fmt.Printf("policy=keep-newest-%d action=%s\n\n", receipt.Keep, action)
	if len(receipt.Kept) == 0 && len(receipt.WouldPrune) == 0 && len(receipt.Pruned) == 0 {
		fmt.Println(style.DimText.Render("no snapshots found"))
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  "+
		style.TableHeader.Render("ACTION")+"\t"+
		style.TableHeader.Render("TIMESTAMP")+"\t"+
		style.TableHeader.Render("CREATED")+"\t"+
		style.TableHeader.Render("COMMIT")+"\t"+
		style.TableHeader.Render("SIZE")+"\t"+
		style.TableHeader.Render("FILE"))
	for _, s := range receipt.Kept {
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n",
			"keep", s.Timestamp, s.CreatedAt, s.CommitSHA, formatBytes(s.Size), s.Filename)
	}
	for _, s := range receipt.WouldPrune {
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n",
			"would-prune", s.Timestamp, s.CreatedAt, s.CommitSHA, formatBytes(s.Size), s.Filename)
	}
	for _, s := range receipt.Pruned {
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n",
			"pruned", s.Timestamp, s.CreatedAt, s.CommitSHA, formatBytes(s.Size), s.Filename)
	}
	w.Flush()
}
