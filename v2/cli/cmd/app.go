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

		deployments, err := client.ListDeployments(appID)
		if err != nil {
			return fmt.Errorf("failed to fetch deployments: %w", err)
		}
		if len(deployments) > 0 {
			latest := deployments[0]
			fmt.Println(style.Subtitle.Render("  deployment"))
			fmt.Printf("  %s %s\n", style.Key.Render("status"), latest.Status)
			if latest.ImageTag != "" {
				fmt.Printf("  %s %s\n", style.Key.Render("image"), latest.ImageTag)
			}
			if latest.CommitSHA != "" {
				fmt.Printf("  %s %s\n", style.Key.Render("commit"), shortValue(latest.CommitSHA, 12))
			}
			if latest.SagaID != "" {
				fmt.Printf("  %s %s\n", style.Key.Render("saga"), shortValue(latest.SagaID, 8))
			}
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

		manifest, err := client.ServiceManifest()
		if err != nil {
			return fmt.Errorf("failed to fetch service manifest: %w", err)
		}
		printAppReachability(appID, manifest)

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

func printAppReachability(appID string, manifest *api.ServiceManifest) {
	var services []api.ServiceManifestEntry
	for _, svc := range manifest.Services {
		if svc.App == appID {
			services = append(services, svc)
		}
	}
	if len(services) == 0 {
		return
	}

	fmt.Println(style.Subtitle.Render("  reachability"))
	if manifest.NetworkMode != "" {
		fmt.Printf("  %s %s\n", style.Key.Render("network"), manifest.NetworkMode)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  "+style.TableHeader.Render("PROCESS")+"\t"+
		style.TableHeader.Render("TYPE")+"\t"+
		style.TableHeader.Render("STATUS")+"\t"+
		style.TableHeader.Render("REACH")+"\t"+
		style.TableHeader.Render("ENDPOINTS")+"\t"+
		style.TableHeader.Render("INSTANCES"))
	for _, svc := range services {
		endpoints := "-"
		if len(svc.Endpoints) > 0 {
			values := make([]string, 0, len(svc.Endpoints))
			for _, endpoint := range svc.Endpoints {
				values = append(values, endpoint.URL)
			}
			endpoints = strings.Join(values, ", ")
		}

		instances := "-"
		if len(svc.Instances) > 0 {
			parts := make([]string, 0, len(svc.Instances))
			for _, inst := range svc.Instances {
				target := inst.Address
				if inst.Port > 0 {
					target = fmt.Sprintf("%s:%d", target, inst.Port)
				}
				if inst.Status != "" {
					target += " " + inst.Status
				}
				parts = append(parts, target)
			}
			instances = strings.Join(parts, ", ")
		}

		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n",
			svc.Process,
			svc.Type,
			renderServiceStatus(svc.Status),
			renderServiceReachability(svc),
			endpoints,
			instances,
		)
	}
	w.Flush()
	fmt.Println()
}

func shortValue(value string, n int) string {
	if len(value) <= n {
		return value
	}
	return value[:n]
}
