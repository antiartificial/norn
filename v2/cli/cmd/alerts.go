package cmd

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

func init() {
	rootCmd.AddCommand(alertsCmd)
}

var alertsCmd = &cobra.Command{
	Use:   "alerts",
	Short: "Show built-in Norn alert rules",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		rules, err := client.AlertRules()
		if err != nil {
			return err
		}
		fmt.Println(style.Title.Render("norn alert rules"))
		if len(rules) == 0 {
			fmt.Println(style.DimText.Render("  no alert rules"))
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, style.TableHeader.Render("ID")+"\t"+
			style.TableHeader.Render("SEVERITY")+"\t"+
			style.TableHeader.Render("EVENTS")+"\t"+
			style.TableHeader.Render("DESCRIPTION"))
		for _, rule := range rules {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
				rule.ID,
				renderSeverity(rule.Severity),
				strings.Join(rule.EventTypes, ","),
				rule.Description,
			)
		}
		w.Flush()
		return nil
	},
}
