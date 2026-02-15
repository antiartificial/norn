package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

func init() {
	rootCmd.AddCommand(cronCmd)
	cronCmd.AddCommand(cronTriggerCmd)
	cronCmd.AddCommand(cronPauseCmd)
	cronCmd.AddCommand(cronResumeCmd)
	cronCmd.AddCommand(cronScheduleCmd)
}

var cronCmd = &cobra.Command{
	Use:   "cron <app>",
	Short: "Manage cron jobs",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]

		states, err := client.CronHistory(appID)
		if err != nil {
			return fmt.Errorf("failed to fetch cron history: %w", err)
		}

		if len(states) == 0 {
			fmt.Println(style.DimText.Render("no cron jobs found"))
			return nil
		}

		fmt.Println(style.Title.Render("cron jobs for " + appID))
		fmt.Println()

		for _, cs := range states {
			dot := style.DotHealthy
			status := "active"
			if cs.Paused {
				dot = style.DotWarning
				status = "paused"
			}
			fmt.Printf("  %s %s  %s  %s\n",
				dot,
				style.Bold.Render(cs.Process),
				style.DimText.Render(cs.Schedule),
				status,
			)

			if len(cs.Runs) > 0 {
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				limit := 5
				if len(cs.Runs) < limit {
					limit = len(cs.Runs)
				}
				for _, run := range cs.Runs[:limit] {
					runDot := style.NomadStatusDot(run.Status)
					fmt.Fprintf(w, "      %s %s\t%s\n",
						runDot, run.StartedAt, run.Status)
				}
				w.Flush()
			}
			fmt.Println()
		}

		return nil
	},
}

var cronTriggerCmd = &cobra.Command{
	Use:   "trigger <app> <process>",
	Short: "Trigger a cron job immediately",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := client.CronTrigger(args[0], args[1]); err != nil {
			return fmt.Errorf("trigger failed: %w", err)
		}
		fmt.Println(style.SuccessBox.Render("triggered " + args[1]))
		return nil
	},
}

var cronPauseCmd = &cobra.Command{
	Use:   "pause <app> <process>",
	Short: "Pause a cron job",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := client.CronPause(args[0], args[1]); err != nil {
			return fmt.Errorf("pause failed: %w", err)
		}
		fmt.Println(style.SuccessBox.Render("paused " + args[1]))
		return nil
	},
}

var cronResumeCmd = &cobra.Command{
	Use:   "resume <app> <process>",
	Short: "Resume a paused cron job",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := client.CronResume(args[0], args[1]); err != nil {
			return fmt.Errorf("resume failed: %w", err)
		}
		fmt.Println(style.SuccessBox.Render("resumed " + args[1]))
		return nil
	},
}

var cronScheduleCmd = &cobra.Command{
	Use:   "schedule <app> <process> <expression>",
	Short: "Update a cron job schedule",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := client.CronUpdateSchedule(args[0], args[1], args[2]); err != nil {
			return fmt.Errorf("update schedule failed: %w", err)
		}
		fmt.Printf("%s schedule updated to: %s\n", style.DotHealthy, args[2])
		return nil
	},
}
