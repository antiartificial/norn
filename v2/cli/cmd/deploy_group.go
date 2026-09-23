package cmd

import (
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"norn/v2/cli/api"
	"norn/v2/cli/style"
)

func init() {
	rootCmd.AddCommand(deployGroupsCmd)
	rootCmd.AddCommand(deployGroupCmd)
	deployGroupCmd.Flags().StringVar(&deployGroupIdempotencyKey, "idempotency-key", "", "Stable retry key (generated and printed when omitted)")
}

var deployGroupIdempotencyKey string

var deployGroupsCmd = &cobra.Command{
	Use:   "deploy-groups",
	Short: "List deploy groups",
	RunE: func(cmd *cobra.Command, args []string) error {
		groups, err := client.ListDeployGroups()
		if err != nil {
			return fmt.Errorf("failed to list deploy groups: %w", err)
		}

		if len(groups) == 0 {
			fmt.Println(style.DimText.Render("no deploy groups configured"))
			return nil
		}

		fmt.Println(style.Title.Render("deploy groups"))
		fmt.Println()

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  "+
			style.TableHeader.Render("GROUP")+"\t"+
			style.TableHeader.Render("APPS")+"\t"+
			style.TableHeader.Render("WAIT READY"))

		for _, g := range groups {
			for i, app := range g.Apps {
				groupName := ""
				if i == 0 {
					groupName = g.Name
				}
				waitReady := style.DimText.Render("no")
				if app.WaitReady {
					waitReady = style.Healthy.Render("yes")
				}
				fmt.Fprintf(w, "  %s\t%s\t%s\n", groupName, app.App, waitReady)
			}
		}
		w.Flush()
		return nil
	},
}

var deployGroupCmd = &cobra.Command{
	Use:   "deploy-group <name> [ref]",
	Short: "Deploy all apps in a group",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		ref := "HEAD"
		if len(args) > 1 {
			ref = args[1]
		}

		fmt.Println(style.Title.Render("deploying group " + name))
		fmt.Printf("  ref: %s\n\n", ref)

		key, err := requestIdempotencyKey(cmd, deployGroupIdempotencyKey, "norn-deploy-group")
		if err != nil {
			return err
		}
		result, err := client.RunDeployGroup(name, ref, key)
		if err != nil {
			return fmt.Errorf("deploy group failed: %w", err)
		}
		return writeDeployGroupResult(os.Stdout, result)
	},
}

func writeDeployGroupResult(output io.Writer, result *api.DeployGroupResult) error {
	w := tabwriter.NewWriter(output, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  "+
		style.TableHeader.Render("APP")+"\t"+
		style.TableHeader.Render("SAGA")+"\t"+
		style.TableHeader.Render("STATUS"))

	failures := 0
	for _, d := range result.Deploys {
		status := style.Healthy.Render("started")
		saga := style.DimText.Render(d.SagaID)
		if d.Error != "" {
			failures++
			status = style.Unhealthy.Render(d.Error)
			saga = style.DimText.Render("-")
		} else if d.Replayed {
			status = style.Healthy.Render("replayed")
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\n", d.App, saga, status)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if failures > 0 {
		return fmt.Errorf("deploy group accepted with %d member enqueue failure(s); retry with the same idempotency key", failures)
	}
	return nil
}
