package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

func init() {
	rootCmd.AddCommand(deployCmd)
	deployCmd.AddCommand(deployStepsCmd)
}

var deployCmd = &cobra.Command{
	Use:   "deploy <app> [ref]",
	Short: "Deploy an app",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]
		ref := "HEAD"
		if len(args) > 1 {
			ref = args[1]
		}

		fmt.Println(style.Title.Render("deploying " + appID))
		fmt.Printf("  ref: %s\n\n", ref)

		sagaID, err := client.Deploy(appID, ref)
		if err != nil {
			return fmt.Errorf("deploy failed: %w", err)
		}

		fmt.Printf("  saga: %s\n\n", style.DimText.Render(sagaID))

		return streamSagaEvents(sagaID)
	},
}

var deployStepsCmd = &cobra.Command{
	Use:   "steps <deployment-id>",
	Short: "Show recorded deploy or rollback stage checkpoints",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		steps, err := client.DeploymentSteps(args[0])
		if err != nil {
			return err
		}
		fmt.Println(style.Title.Render("deployment steps"))
		if len(steps) == 0 {
			fmt.Println(style.DimText.Render("  no steps recorded"))
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, style.TableHeader.Render("STEP")+"\t"+
			style.TableHeader.Render("STATUS")+"\t"+
			style.TableHeader.Render("ATTEMPT")+"\t"+
			style.TableHeader.Render("STARTED")+"\t"+
			style.TableHeader.Render("MS")+"\t"+
			style.TableHeader.Render("MESSAGE"))
		for _, step := range steps {
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%d\t%s\n",
				step.Step,
				step.Status,
				step.Attempt,
				localTime(step.StartedAt),
				step.DurationMs,
				emptyDash(step.Message),
			)
		}
		w.Flush()
		return nil
	},
}
