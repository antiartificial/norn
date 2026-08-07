package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"norn/v2/cli/api"
	"norn/v2/cli/style"
)

var (
	queuedUpgradeMode  string
	queuedDrainMode    string
	maintenanceWait    bool
	maintenanceTimeout time.Duration
)

func init() {
	platformCmd.AddCommand(platformQueuePreflightCmd)
	platformCmd.AddCommand(platformQueueUpgradeCmd)
	platformCmd.AddCommand(platformQueueSmokeCmd)
	platformCmd.AddCommand(platformQueueRollbackCmd)
	hostCmd.AddCommand(hostQueueAssureCmd)

	for _, command := range []*cobra.Command{platformQueuePreflightCmd, platformQueueUpgradeCmd, platformQueueRollbackCmd, platformQueueSmokeCmd, hostQueueAssureCmd} {
		command.Flags().BoolVar(&maintenanceWait, "wait", true, "Wait for the durable operation to finish")
		command.Flags().DurationVar(&maintenanceTimeout, "timeout", 2*time.Hour, "Maximum time to wait")
	}
	platformQueueUpgradeCmd.Flags().StringVar(&queuedUpgradeMode, "mode", "restart", "Upgrade mode: restart or proxy")
	platformQueueUpgradeCmd.Flags().StringVar(&queuedDrainMode, "drain", "fail", "Drain behavior: fail, wait, or force")
}

var platformQueuePreflightCmd = &cobra.Command{
	Use:   "queue-preflight [ref]",
	Short: "Queue platform preflight through the host maintenance agent",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ref := "HEAD"
		if len(args) == 1 {
			ref = args[0]
		}
		op, err := client.QueuePlatformPreflight(ref)
		return handleMaintenanceOperation(cmd, op, err)
	},
}

var platformQueueUpgradeCmd = &cobra.Command{
	Use:   "queue-upgrade [ref]",
	Short: "Queue a restart-safe platform upgrade through the host maintenance agent",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ref := "HEAD"
		if len(args) == 1 {
			ref = args[0]
		}
		op, err := client.QueuePlatformUpgrade(ref, queuedUpgradeMode, queuedDrainMode)
		return handleMaintenanceOperation(cmd, op, err)
	},
}

var platformQueueSmokeCmd = &cobra.Command{
	Use:   "queue-smoke",
	Short: "Queue authenticated platform smoke through the host maintenance agent",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		op, err := client.QueuePlatformSmoke()
		return handleMaintenanceOperation(cmd, op, err)
	},
}

var platformQueueRollbackCmd = &cobra.Command{
	Use:   "queue-rollback <sha-prefix>",
	Short: "Queue a restart-safe platform rollback through the host maintenance agent",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		op, err := client.QueuePlatformRollback(args[0])
		return handleMaintenanceOperation(cmd, op, err)
	},
}

var hostQueueAssureCmd = &cobra.Command{
	Use:   "queue-assure",
	Short: "Queue host assurance through the independent host maintenance agent",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		op, err := client.QueueHostAssurance()
		return handleMaintenanceOperation(cmd, op, err)
	},
}

func handleMaintenanceOperation(cmd *cobra.Command, op *api.Operation, err error) error {
	if err != nil {
		return err
	}
	fmt.Printf("%s %s (%s)\n", style.Healthy.Render("queued"), op.ID, op.Kind)
	if !maintenanceWait {
		return nil
	}
	deadline := time.Now().Add(maintenanceTimeout)
	lastStatus := op.Status
	for {
		if op.Status == "succeeded" {
			fmt.Println(style.SuccessBox.Render(op.Message))
			return nil
		}
		if op.Status == "failed" || op.Status == "canceled" {
			return fmt.Errorf("%s: %s", op.Status, op.Message)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for operation %s", op.ID)
		}
		select {
		case <-cmd.Context().Done():
			return cmd.Context().Err()
		case <-time.After(2 * time.Second):
		}
		updated, getErr := client.GetOperation(op.ID)
		if getErr != nil {
			continue
		}
		op = updated
		if op.Status != lastStatus {
			fmt.Printf("%s %s\n", op.Status, op.Message)
			lastStatus = op.Status
		}
	}
}
