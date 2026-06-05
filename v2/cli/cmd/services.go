package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"norn/v2/cli/api"
	"norn/v2/cli/style"
)

func init() {
	rootCmd.AddCommand(servicesCmd)
	servicesCmd.AddCommand(servicesManifestCmd)
}

var servicesCmd = &cobra.Command{
	Use:   "services",
	Short: "Inspect hosted services and process reachability",
	RunE: func(cmd *cobra.Command, args []string) error {
		manifest, err := client.ServiceManifest()
		if err != nil {
			return fmt.Errorf("failed to fetch service manifest: %w", err)
		}
		printServices(manifest)
		return nil
	},
}

var servicesManifestCmd = &cobra.Command{
	Use:   "manifest",
	Short: "Print the raw service manifest JSON",
	RunE: func(cmd *cobra.Command, args []string) error {
		manifest, err := client.ServiceManifest()
		if err != nil {
			return fmt.Errorf("failed to fetch service manifest: %w", err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(manifest)
	},
}

func printServices(manifest *api.ServiceManifest) {
	if len(manifest.Services) == 0 {
		fmt.Println(style.DimText.Render("no services discovered"))
		return
	}

	fmt.Println(style.Title.Render("hosted services"))
	fmt.Println()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, style.TableHeader.Render("SERVICE")+"\t"+
		style.TableHeader.Render("APP")+"\t"+
		style.TableHeader.Render("PROCESS")+"\t"+
		style.TableHeader.Render("TYPE")+"\t"+
		style.TableHeader.Render("STATUS")+"\t"+
		style.TableHeader.Render("REACH")+"\t"+
		style.TableHeader.Render("ENDPOINTS")+"\t"+
		style.TableHeader.Render("INSTANCES"))

	for _, svc := range manifest.Services {
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

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			style.Bold.Render(svc.Name),
			svc.App,
			svc.Process,
			svc.Type,
			renderServiceStatus(svc.Status),
			renderServiceReachability(svc),
			endpoints,
			instances,
		)
	}
	w.Flush()
}

func renderServiceReachability(svc api.ServiceManifestEntry) string {
	endpointScope := svc.Metadata["endpointScope"]
	instanceScope := svc.Metadata["instanceScope"]
	if endpointScope == "" {
		endpointScope = "unknown"
	}
	if instanceScope == "" {
		instanceScope = "unknown"
	}
	if endpointScope == "none" {
		return "internal/" + instanceScope
	}
	if endpointScope == instanceScope {
		return endpointScope
	}
	return endpointScope + "/" + instanceScope
}

func renderServiceStatus(status string) string {
	switch status {
	case "passing":
		return style.Healthy.Render(status)
	case "critical":
		return style.Unhealthy.Render(status)
	case "warning":
		return style.Warning.Render(status)
	default:
		return style.DimText.Render(status)
	}
}
