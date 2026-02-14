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
	rootCmd.AddCommand(appCmd)
}

var appCmd = &cobra.Command{
	Use:   "app <id>",
	Short: "Show details for a specific app",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]

		app, err := client.GetApp(appID)
		if err != nil {
			return fmt.Errorf("failed to fetch app: %w", err)
		}

		// Header: name + status dot
		dot := style.NomadStatusDot(app.NomadStatus)
		fmt.Printf("%s %s  %s\n\n", dot, style.Title.Render(app.Spec.App), style.DimText.Render(app.NomadStatus))

		// Repo info
		if app.Spec.Repo != nil {
			fmt.Printf("  %s %s", style.Key.Render("repo"), app.Spec.Repo.URL)
			if app.Spec.Repo.Branch != "" {
				fmt.Printf(" (%s)", app.Spec.Repo.Branch)
			}
			fmt.Println()
			fmt.Println()
		}

		// Processes table
		if len(app.Spec.Processes) > 0 {
			fmt.Println(style.Subtitle.Render("  processes"))
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "  "+style.TableHeader.Render("NAME")+"\t"+
				style.TableHeader.Render("PORT")+"\t"+
				style.TableHeader.Render("COMMAND"))
			for name, proc := range app.Spec.Processes {
				port := "-"
				if proc.Port > 0 {
					port = fmt.Sprintf("%d", proc.Port)
				}
				command := proc.Command
				if command == "" && proc.Schedule != "" {
					command = fmt.Sprintf("schedule: %s", proc.Schedule)
				}
				if len(command) > 60 {
					command = command[:57] + "..."
				}
				fmt.Fprintf(w, "  %s\t%s\t%s\n",
					style.Bold.Render(name),
					port,
					style.DimText.Render(command),
				)
			}
			w.Flush()
			fmt.Println()
		}

		// Allocations table
		if len(app.Allocations) > 0 {
			fmt.Println(style.Subtitle.Render("  allocations"))
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "  "+style.TableHeader.Render("ID")+"\t"+
				style.TableHeader.Render("GROUP")+"\t"+
				style.TableHeader.Render("STATUS")+"\t"+
				style.TableHeader.Render("NODE"))
			for _, alloc := range app.Allocations {
				shortID := alloc.ID
				if len(shortID) > 8 {
					shortID = shortID[:8]
				}
				node := alloc.NodeID
				if len(node) > 8 {
					node = node[:8]
				}
				statusStyle := style.DimText
				if alloc.Status == "running" {
					statusStyle = style.Healthy
				} else if alloc.Status == "failed" || alloc.Status == "lost" {
					statusStyle = style.Unhealthy
				}
				fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n",
					style.DimText.Render(shortID),
					alloc.TaskGroup,
					statusStyle.Render(alloc.Status),
					style.DimText.Render(node),
				)
			}
			w.Flush()

			// Summary line
			running := 0
			for _, a := range app.Allocations {
				if a.Status == "running" {
					running++
				}
			}
			// Group breakdown
			groups := map[string]int{}
			for _, a := range app.Allocations {
				groups[a.TaskGroup]++
			}
			var parts []string
			for g, c := range groups {
				parts = append(parts, fmt.Sprintf("%s=%d", g, c))
			}
			fmt.Printf("\n  %s %d/%d running  %s\n",
				style.StatusDot(app.Healthy),
				running, len(app.Allocations),
				style.DimText.Render(strings.Join(parts, " ")),
			)
		}

		return nil
	},
}
