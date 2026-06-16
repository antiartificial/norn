package cmd

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

var (
	notifyToken   string
	notifyUserKey string
	notifySeverity []string
)

func init() {
	notificationsCmd.AddCommand(notificationsListCmd)
	notificationsCmd.AddCommand(notificationsAddCmd)
	notificationsCmd.AddCommand(notificationsRemoveCmd)
	notificationsCmd.AddCommand(notificationsTestCmd)
	notificationsCmd.AddCommand(notificationsBootstrapCmd)

	notificationsAddCmd.Flags().StringVar(&notifyToken, "token", "", "API token (for pushover)")
	notificationsAddCmd.Flags().StringVar(&notifyUserKey, "user-key", "", "User key (for pushover)")
	notificationsAddCmd.Flags().StringSliceVar(&notifySeverity, "severity", nil, "Severity filter (info, warning, critical)")

	rootCmd.AddCommand(notificationsCmd)
}

var notificationsCmd = &cobra.Command{
	Use:   "notifications",
	Short: "Manage notification channels",
}

var notificationsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List notification channels",
	RunE: func(cmd *cobra.Command, args []string) error {
		channels, err := client.ListNotificationChannels()
		if err != nil {
			return fmt.Errorf("failed to list notification channels: %w", err)
		}

		if len(channels) == 0 {
			fmt.Println(style.DimText.Render("no notification channels configured"))
			return nil
		}

		fmt.Println(style.Title.Render("notification channels"))
		fmt.Println()

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  "+
			style.TableHeader.Render("ID")+"\t"+
			style.TableHeader.Render("PROVIDER")+"\t"+
			style.TableHeader.Render("NAME")+"\t"+
			style.TableHeader.Render("SEVERITIES"))

		for _, ch := range channels {
			severities := "-"
			if len(ch.Severities) > 0 {
				severities = strings.Join(ch.Severities, ", ")
			}
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n",
				ch.ID, ch.Provider, ch.Name, severities)
		}
		w.Flush()
		return nil
	},
}

var notificationsAddCmd = &cobra.Command{
	Use:   "add <provider> <name> <url>",
	Short: "Add a notification channel (discord, ntfy, pushover, webhook)",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		provider := args[0]
		name := args[1]
		channelURL := args[2]

		ch, err := client.CreateNotificationChannel(provider, name, channelURL, notifyToken, notifyUserKey, notifySeverity)
		if err != nil {
			return fmt.Errorf("failed to create notification channel: %w", err)
		}

		fmt.Println(style.SuccessBox.Render("notification channel created"))
		fmt.Printf("  %s %s\n", style.Key.Render("id"), ch.ID)
		fmt.Printf("  %s %s\n", style.Key.Render("provider"), ch.Provider)
		fmt.Printf("  %s %s\n", style.Key.Render("name"), ch.Name)
		return nil
	},
}

var notificationsRemoveCmd = &cobra.Command{
	Use:   "remove <id>",
	Short: "Remove a notification channel",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := client.DeleteNotificationChannel(args[0]); err != nil {
			return fmt.Errorf("failed to delete notification channel: %w", err)
		}
		fmt.Println(style.SuccessBox.Render("notification channel removed"))
		return nil
	},
}

var notificationsTestCmd = &cobra.Command{
	Use:   "test <id>",
	Short: "Send a test notification to a channel",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := client.TestNotificationChannel(args[0]); err != nil {
			return fmt.Errorf("test notification failed: %w", err)
		}
		fmt.Println(style.SuccessBox.Render("test notification sent"))
		return nil
	},
}

var notificationsBootstrapCmd = &cobra.Command{
	Use:   "bootstrap",
	Short: "Auto-discover services and create default notification channels",
	RunE: func(cmd *cobra.Command, args []string) error {
		result, err := client.BootstrapNotificationChannels()
		if err != nil {
			return fmt.Errorf("bootstrap failed: %w", err)
		}

		for _, msg := range result.Skipped {
			fmt.Printf("  %s %s\n", style.DimText.Render("skip"), msg)
		}
		if len(result.Created) == 0 && len(result.Skipped) == 0 {
			fmt.Println(style.DimText.Render("no channels to bootstrap (vigil-gateway not found)"))
			return nil
		}
		for _, ch := range result.Created {
			fmt.Println(style.SuccessBox.Render("created " + ch.Provider + " channel"))
			fmt.Printf("  %s %s\n", style.Key.Render("id"), ch.ID)
			fmt.Printf("  %s %s\n", style.Key.Render("name"), ch.Name)
			fmt.Printf("  %s %s\n", style.Key.Render("url"), ch.URL)
			fmt.Printf("  %s %s\n", style.Key.Render("severities"), strings.Join(ch.Severities, ", "))
		}
		return nil
	},
}
