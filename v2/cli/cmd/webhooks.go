package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"norn/v2/cli/api"
	"norn/v2/cli/style"
)

var (
	webhookLimit           int
	webhookReplayPreflight bool
	webhookIdempotencyKey  string
)

func init() {
	rootCmd.AddCommand(webhooksCmd)
	webhooksCmd.Flags().IntVar(&webhookLimit, "limit", 25, "Maximum deliveries to show")
	webhooksCmd.AddCommand(webhooksReplayCmd)
	webhooksReplayCmd.Flags().BoolVar(&webhookReplayPreflight, "preflight", false, "Queue a preflight instead of a deploy")
	webhooksReplayCmd.Flags().StringVar(&webhookIdempotencyKey, "idempotency-key", "", "Stable retry key (generated and printed when omitted)")
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

var webhooksReplayCmd = &cobra.Command{
	Use:   "replay <delivery-id>",
	Short: "Replay a webhook delivery through the durable operation queue",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mode := "deploy"
		if webhookReplayPreflight {
			mode = "preflight"
		}
		key, err := requestIdempotencyKey(cmd, webhookIdempotencyKey, "norn-webhook-replay")
		if err != nil {
			return err
		}
		resp, err := client.ReplayWebhookDelivery(args[0], mode, key)
		if err != nil {
			return err
		}
		if resp.Status == "failed" || resp.Status == "canceled" {
			return fmt.Errorf("operation %s %s", resp.OperationID, resp.Status)
		}
		fmt.Printf("%s queued %s for %s saga=%s\n", style.Healthy.Render("replay"), resp.Mode, resp.App, resp.SagaID)
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
