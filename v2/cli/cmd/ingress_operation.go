package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"norn/v2/cli/api"
	"norn/v2/cli/style"
)

var ingressIdempotencyKey string
var ingressWait = true
var ingressTimeout = 5 * time.Minute

func addIngressOperationFlags(command *cobra.Command) {
	command.Flags().StringVar(&ingressIdempotencyKey, "idempotency-key", "", "Stable retry key (generated and printed when omitted)")
	command.Flags().BoolVar(&ingressWait, "wait", true, "Wait for the queued ingress mutation")
	command.Flags().DurationVar(&ingressTimeout, "timeout", 5*time.Minute, "Maximum time to wait for the ingress mutation")
}

func handleIngressOperation(command *cobra.Command, operation *api.Operation, action string) error {
	if operation == nil {
		return fmt.Errorf("%s returned no response", action)
	}
	if operation.ID == "" {
		switch operation.Status {
		case "unchanged", "skipped":
			fmt.Fprintf(command.OutOrStdout(), "%s: %s\n", action, operation.Status)
			return nil
		default:
			return fmt.Errorf("%s acceptance returned no operation id", action)
		}
	}
	if operation.Status == "queued" || operation.Status == "running" {
		fmt.Fprintf(command.OutOrStdout(), "%s queued as operation %s (%s)\n", action, operation.ID, operation.Status)
	} else {
		fmt.Fprintf(command.OutOrStdout(), "%s operation %s is %s\n", action, operation.ID, operation.Status)
	}
	if operation.Status == "failed" || operation.Status == "canceled" {
		return fmt.Errorf("%s %s: %s (operation %s)", action, operation.Status, operation.Message, operation.ID)
	}
	if operation.Status == "succeeded" {
		fmt.Fprintln(command.OutOrStdout(), style.SuccessBox.Render(action+" completed"))
		return nil
	}
	if !ingressWait {
		fmt.Fprintf(command.OutOrStdout(), "Check status with: norn operations %s\n", operation.ID)
		return nil
	}
	if ingressTimeout <= 0 {
		return fmt.Errorf("--timeout must be positive")
	}
	deadline := time.Now().Add(ingressTimeout)
	for {
		switch operation.Status {
		case "succeeded":
			fmt.Fprintln(command.OutOrStdout(), style.SuccessBox.Render(action+" completed"))
			return nil
		case "failed", "canceled":
			return fmt.Errorf("%s %s: %s (operation %s)", action, operation.Status, operation.Message, operation.ID)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s operation %s is still %s; check its status with norn operations %s", action, operation.ID, operation.Status, operation.ID)
		}
		select {
		case <-command.Context().Done():
			return command.Context().Err()
		case <-time.After(2 * time.Second):
		}
		updated, err := client.GetOperation(operation.ID)
		if err != nil {
			return fmt.Errorf("%s operation %s continues; status lookup failed: %w", action, operation.ID, err)
		}
		operation = updated
	}
}
