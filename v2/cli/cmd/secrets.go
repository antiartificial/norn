package cmd

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"norn/v2/cli/api"
	"norn/v2/cli/style"
)

func init() {
	secretsCmd.AddCommand(secretsListCmd)
	secretsCmd.AddCommand(secretsStatusCmd)
	secretsCmd.AddCommand(secretsMigratePlanCmd)
	secretsCmd.AddCommand(secretsSetCmd)
	secretsCmd.AddCommand(secretsDeleteCmd)
	rootCmd.AddCommand(secretsCmd)
}

var secretsCmd = &cobra.Command{
	Use:   "secrets <app>",
	Short: "Manage secrets for an app",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Default to list for backwards compat
		return listSecrets(args[0])
	},
}

var secretsListCmd = &cobra.Command{
	Use:   "list <app>",
	Short: "List secret keys for an app",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return listSecrets(args[0])
	},
}

var secretsStatusCmd = &cobra.Command{
	Use:   "status [app]",
	Short: "Compare declared, encrypted, and plaintext secret state",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 {
			status, err := client.SecretsStatusApp(args[0])
			if err != nil {
				return fmt.Errorf("failed to fetch secrets status: %w", err)
			}
			printSecretStatusDetail(*status)
			return nil
		}
		statuses, err := client.SecretsStatusAll()
		if err != nil {
			return fmt.Errorf("failed to fetch secrets status: %w", err)
		}
		printSecretStatusTable(statuses)
		return nil
	},
}

var secretsMigratePlanCmd = &cobra.Command{
	Use:   "migrate-plan [app]",
	Short: "Plan plaintext env migration into encrypted secrets",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := ""
		if len(args) == 1 {
			appID = args[0]
		}
		plan, err := client.SecretsMigrationPlan(appID)
		if err != nil {
			return fmt.Errorf("failed to fetch migration plan: %w", err)
		}
		printSecretMigrationPlan(plan)
		return nil
	},
}

var secretsSetCmd = &cobra.Command{
	Use:   "set <app> KEY=VALUE ...",
	Short: "Set secrets for an app",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]
		secrets := make(map[string]string)
		for _, kv := range args[1:] {
			parts := strings.SplitN(kv, "=", 2)
			if len(parts) != 2 {
				return fmt.Errorf("invalid format %q, expected KEY=VALUE", kv)
			}
			secrets[parts[0]] = parts[1]
		}

		if err := client.UpdateSecrets(appID, secrets); err != nil {
			return fmt.Errorf("failed to set secrets: %w", err)
		}

		fmt.Println(style.SuccessBox.Render(fmt.Sprintf("set %d secret(s) for %s", len(secrets), appID)))
		return nil
	},
}

var secretsDeleteCmd = &cobra.Command{
	Use:   "delete <app> KEY ...",
	Short: "Delete secrets from an app",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]
		for _, key := range args[1:] {
			if err := client.DeleteSecret(appID, key); err != nil {
				return fmt.Errorf("failed to delete %s: %w", key, err)
			}
			fmt.Printf("  %s deleted %s\n", style.StepDone.Render("✓"), key)
		}
		return nil
	},
}

func listSecrets(appID string) error {
	secrets, err := client.ListSecrets(appID)
	if err != nil {
		return fmt.Errorf("failed to fetch secrets: %w", err)
	}

	fmt.Println(style.Title.Render("secrets for " + appID))

	if len(secrets) == 0 {
		fmt.Println(style.DimText.Render("  no secrets configured"))
		return nil
	}

	for _, s := range secrets {
		fmt.Printf("  %s %s\n", style.DimText.Render("•"), s)
	}
	return nil
}

func printSecretStatusTable(statuses []api.SecretStatus) {
	if len(statuses) == 0 {
		fmt.Println(style.DimText.Render("no apps discovered"))
		return
	}

	fmt.Println(style.Title.Render("secrets status"))
	fmt.Println()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, style.TableHeader.Render("APP")+"\t"+
		style.TableHeader.Render("STATUS")+"\t"+
		style.TableHeader.Render("DECLARED")+"\t"+
		style.TableHeader.Render("ENCRYPTED")+"\t"+
		style.TableHeader.Render("MISSING")+"\t"+
		style.TableHeader.Render("UNUSED")+"\t"+
		style.TableHeader.Render("PLAINTEXT"))
	for _, status := range statuses {
		state := style.Healthy.Render("ok")
		if !status.OK {
			state = style.Warning.Render("needs-attention")
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%d\t%d\n",
			status.App,
			state,
			len(status.Declared),
			len(status.Encrypted),
			len(status.MissingEncrypted),
			len(status.EncryptedUndeclared),
			len(status.PlainEnvWarnings),
		)
	}
	w.Flush()
}

func printSecretStatusDetail(status api.SecretStatus) {
	state := style.Healthy.Render("ok")
	if !status.OK {
		state = style.Warning.Render("needs attention")
	}
	fmt.Println(style.Title.Render("secrets status for " + status.App))
	fmt.Println("status:", state)
	fmt.Println()
	printSecretKeyList("declared", status.Declared)
	printSecretKeyList("encrypted", status.Encrypted)
	printSecretKeyList("missing encrypted values", status.MissingEncrypted)
	printSecretKeyList("encrypted but undeclared", status.EncryptedUndeclared)
	printSecretKeyList("plaintext env warnings", status.PlainEnvWarnings)
}

func printSecretKeyList(title string, values []string) {
	fmt.Println(style.Bold.Render(title))
	if len(values) == 0 {
		fmt.Println(style.DimText.Render("  none"))
		return
	}
	for _, value := range values {
		fmt.Printf("  %s %s\n", style.DimText.Render("•"), value)
	}
}

func printSecretMigrationPlan(plan *api.SecretMigrationPlan) {
	title := "secrets migration plan"
	if plan.App != "" {
		title += " for " + plan.App
	}
	fmt.Println(style.Title.Render(title))
	fmt.Printf("generated=%s items=%d\n\n", plan.GeneratedAt, plan.Count)
	if len(plan.Items) == 0 {
		fmt.Println(style.DimText.Render("no plaintext secret-like env entries found"))
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, style.TableHeader.Render("APP")+"\t"+
		style.TableHeader.Render("KEY")+"\t"+
		style.TableHeader.Render("FIELD")+"\t"+
		style.TableHeader.Render("DECLARED")+"\t"+
		style.TableHeader.Render("ENCRYPTED")+"\t"+
		style.TableHeader.Render("ACTION"))
	for _, item := range plan.Items {
		fmt.Fprintf(w, "%s\t%s\t%s\t%t\t%t\t%s\n",
			item.App,
			item.Key,
			item.Field,
			item.Declared,
			item.Encrypted,
			item.Action,
		)
	}
	w.Flush()
}
