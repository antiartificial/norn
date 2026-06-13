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
	operationsActive bool
	operationsLimit  int
)

func init() {
	rootCmd.AddCommand(operationsCmd)
	operationsCmd.Flags().BoolVar(&operationsActive, "active", false, "Only show queued/running operations")
	operationsCmd.Flags().IntVar(&operationsLimit, "limit", 25, "Maximum operations to show")
}

var operationsCmd = &cobra.Command{
	Use:     "operations",
	Aliases: []string{"opslog", "oplog"},
	Short:   "List durable Norn operation records",
	RunE: func(cmd *cobra.Command, args []string) error {
		ops, err := client.ListOperations(operationsActive, operationsLimit)
		if err != nil {
			return err
		}
		printOperations(ops)
		return nil
	},
}

func printOperations(ops []api.Operation) {
	fmt.Println(style.Title.Render("operations"))
	if len(ops) == 0 {
		fmt.Println(style.DimText.Render("  no operations"))
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, style.TableHeader.Render("TIME")+"\t"+
		style.TableHeader.Render("STATUS")+"\t"+
		style.TableHeader.Render("KIND")+"\t"+
		style.TableHeader.Render("APP")+"\t"+
		style.TableHeader.Render("REF")+"\t"+
		style.TableHeader.Render("RISK")+"\t"+
		style.TableHeader.Render("MESSAGE"))
	for _, op := range ops {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			localTime(op.StartedAt),
			op.Status,
			op.Kind,
			emptyDash(op.App),
			shortValue(op.Ref, 12),
			emptyDash(op.Risk),
			emptyDash(op.Message),
		)
	}
	w.Flush()
}
