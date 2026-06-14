package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

var (
	grantIP   string
	grantTTL  string
	grantNote string
)

func init() {
	accessGrantCmd.Flags().StringVar(&grantIP, "ip", "", "IP address to grant access (required)")
	accessGrantCmd.Flags().StringVar(&grantTTL, "ttl", "", "Duration of the grant, e.g. 24h (required)")
	accessGrantCmd.Flags().StringVar(&grantNote, "note", "", "Reason or description for the grant")
	_ = accessGrantCmd.MarkFlagRequired("ip")
	_ = accessGrantCmd.MarkFlagRequired("ttl")

	accessCmd.AddCommand(accessGrantCmd)
	accessCmd.AddCommand(accessGrantsCmd)
	accessCmd.AddCommand(accessRevokeCmd)
}

var accessGrantCmd = &cobra.Command{
	Use:   "grant",
	Short: "Create a temporary IP access grant",
	Example: "  norn access grant --ip 1.2.3.4 --ttl 24h --note \"CI server\"",
	RunE: func(cmd *cobra.Command, args []string) error {
		grant, err := client.CreateAccessGrant(grantIP, grantNote, grantTTL)
		if err != nil {
			return fmt.Errorf("failed to create access grant: %w", err)
		}
		msg := fmt.Sprintf(
			"%s %s\n%s %s\n%s %s",
			style.Key.Render("id"),
			grant.ID,
			style.Key.Render("ip"),
			grant.IP,
			style.Key.Render("expires"),
			localTime(grant.ExpiresAt),
		)
		fmt.Println(style.SuccessBox.Render("access grant created\n\n" + msg))
		return nil
	},
}

var accessGrantsCmd = &cobra.Command{
	Use:   "grants",
	Short: "List active IP access grants",
	RunE: func(cmd *cobra.Command, args []string) error {
		grants, err := client.ListAccessGrants()
		if err != nil {
			return fmt.Errorf("failed to list access grants: %w", err)
		}
		if len(grants) == 0 {
			fmt.Println(style.DimText.Render("no active access grants"))
			return nil
		}
		fmt.Println(style.Title.Render("access grants"))
		fmt.Println()
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  "+
			style.TableHeader.Render("ID")+"\t"+
			style.TableHeader.Render("IP")+"\t"+
			style.TableHeader.Render("NOTE")+"\t"+
			style.TableHeader.Render("CREATED")+"\t"+
			style.TableHeader.Render("EXPIRES"))
		for _, g := range grants {
			note := g.Note
			if note == "" {
				note = style.DimText.Render("-")
			}
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n",
				g.ID,
				g.IP,
				note,
				localTime(g.CreatedAt),
				localTime(g.ExpiresAt),
			)
		}
		return w.Flush()
	},
}

var accessRevokeCmd = &cobra.Command{
	Use:   "revoke <grant-id>",
	Short: "Revoke an IP access grant",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		if err := client.DeleteAccessGrant(id); err != nil {
			return fmt.Errorf("failed to revoke access grant: %w", err)
		}
		fmt.Println(style.SuccessBox.Render(fmt.Sprintf("access grant revoked\n\n%s %s", style.Key.Render("id"), id)))
		return nil
	},
}
