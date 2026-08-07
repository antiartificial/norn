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
	Use:     "operations [operation-id]",
	Aliases: []string{"opslog", "oplog"},
	Short:   "List durable Norn operation records",
	Args:    cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 {
			op, err := client.GetOperation(args[0])
			if err != nil {
				return err
			}
			printOperations([]api.Operation{*op})
			return nil
		}
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
		style.TableHeader.Render("ID")+"\t"+
		style.TableHeader.Render("STATUS")+"\t"+
		style.TableHeader.Render("KIND")+"\t"+
		style.TableHeader.Render("APP")+"\t"+
		style.TableHeader.Render("REF")+"\t"+
		style.TableHeader.Render("TRY")+"\t"+
		style.TableHeader.Render("RISK")+"\t"+
		style.TableHeader.Render("MESSAGE"))
	for _, op := range ops {
		attempts := "-"
		if op.MaxAttempts > 0 {
			attempts = fmt.Sprintf("%d/%d", op.Attempts, op.MaxAttempts)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			localTime(op.StartedAt),
			shortValue(op.ID, 12),
			op.Status,
			op.Kind,
			emptyDash(op.App),
			shortValue(op.Ref, 12),
			attempts,
			emptyDash(op.Risk),
			emptyDash(op.Message),
		)
	}
	w.Flush()
}
