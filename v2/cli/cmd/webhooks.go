package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"norn/v2/cli/api"
	"norn/v2/cli/style"
)

var webhookLimit int

func init() {
	rootCmd.AddCommand(webhooksCmd)
	webhooksCmd.Flags().IntVar(&webhookLimit, "limit", 25, "Maximum deliveries to show")
}

var webhooksCmd = &cobra.Command{
	Use:   "webhooks",
	Short: "List recent webhook deliveries",
	RunE: func(cmd *cobra.Command, args []string) error {
		deliveries, err := client.ListWebhookDeliveries(webhookLimit)
		if err != nil {
			return err
		}
		printWebhookDeliveries(deliveries)
		return nil
	},
}

func printWebhookDeliveries(deliveries []api.WebhookDelivery) {
	fmt.Println(style.Title.Render("webhooks"))
	if len(deliveries) == 0 {
		fmt.Println(style.DimText.Render("  no webhook deliveries"))
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, style.TableHeader.Render("TIME")+"\t"+
		style.TableHeader.Render("STATUS")+"\t"+
		style.TableHeader.Render("PROVIDER")+"\t"+
		style.TableHeader.Render("EVENT")+"\t"+
		style.TableHeader.Render("APP")+"\t"+
		style.TableHeader.Render("BRANCH")+"\t"+
		style.TableHeader.Render("REASON"))
	for _, delivery := range deliveries {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			localTime(delivery.ReceivedAt),
			delivery.Status,
			delivery.Provider,
			emptyDash(delivery.Event),
			emptyDash(delivery.App),
			emptyDash(delivery.Branch),
			emptyDash(delivery.Reason),
		)
	}
	w.Flush()
}
