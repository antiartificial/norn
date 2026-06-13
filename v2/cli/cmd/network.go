package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"norn/v2/cli/api"
	"norn/v2/cli/style"
)

func init() {
	rootCmd.AddCommand(networkCmd)
}

var networkCmd = &cobra.Command{
	Use:   "network",
	Short: "Summarize Norn service reachability and network-mode guidance",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		manifest, err := client.ServiceManifest()
		if err != nil {
			return fmt.Errorf("failed to fetch service manifest: %w", err)
		}
		results, err := client.ValidateAll(false)
		if err != nil {
			return fmt.Errorf("failed to fetch validation findings: %w", err)
		}
		printNetworkTruth(manifest, results)
		return nil
	},
}

func printNetworkTruth(manifest *api.ServiceManifest, results []api.ValidationResult) {
	mode := manifest.NetworkMode
	if mode == "" {
		mode = "unknown"
	}
	fmt.Println(style.Title.Render("norn network truth"))
	fmt.Printf("mode=%s services=%d\n", mode, len(manifest.Services))
	fmt.Println()

	counts := map[string]int{}
	for _, svc := range manifest.Services {
		exposure := svc.Reachability.Exposure
		if exposure == "" {
			exposure = "unknown"
		}
		counts[exposure]++
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Printf("%s=%d ", key, counts[key])
	}
	fmt.Println()
	fmt.Println()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, style.TableHeader.Render("APP")+"\t"+
		style.TableHeader.Render("PROCESS")+"\t"+
		style.TableHeader.Render("TYPE")+"\t"+
		style.TableHeader.Render("EXPOSURE")+"\t"+
		style.TableHeader.Render("ENDPOINT")+"\t"+
		style.TableHeader.Render("INSTANCE"))
	for _, svc := range manifest.Services {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			svc.App,
			svc.Process,
			svc.Type,
			emptyDash(svc.Reachability.Exposure),
			emptyDash(svc.Reachability.EndpointScope),
			emptyDash(svc.Reachability.InstanceScope),
		)
	}
	w.Flush()
	fmt.Println()

	findings := networkValidationFindings(results)
	if len(findings) > 0 {
		fmt.Println(style.Subtitle.Render("  validation hints"))
		for _, finding := range findings {
			fmt.Printf("  %s\n", finding)
		}
		fmt.Println()
	}

	fmt.Println(style.Subtitle.Render("  mode guidance"))
	for _, line := range networkGuidance(mode) {
		fmt.Printf("  %s\n", line)
	}
}

func networkValidationFindings(results []api.ValidationResult) []string {
	var findings []string
	for _, result := range results {
		for _, finding := range result.Findings {
			if strings.HasPrefix(finding.Field, "endpoints[") {
				findings = append(findings, fmt.Sprintf("%s %s: %s", result.App, finding.Field, finding.Message))
			}
		}
	}
	sort.Strings(findings)
	return findings
}

func networkGuidance(mode string) []string {
	switch strings.ToLower(mode) {
	case "tailnet", "tailscale":
		return []string{
			"Use Tailscale hostnames or private tailnet IPs for operator-facing services.",
			"Use cloudflared hostnames only for public services that need internet reachability.",
			"Avoid 127.0.0.1 endpoints unless the caller is on the same host.",
			"Use host.docker.internal when a container must call a host-local Norn API.",
		}
	case "public":
		return []string{
			"Use cloudflared or another public ingress for user-facing endpoints.",
			"Private tailnet endpoints are not generally reachable from public clients.",
			"Keep internal workers endpoint-free and inspect them through service instances.",
		}
	default:
		return []string{
			"Use 127.0.0.1 or localhost for host-local development endpoints.",
			"Use host.docker.internal when a container must call a host-local service.",
			"Public hostnames require cloudflared/forge routing before they are reachable.",
			"Worker, cron, and function processes should usually remain endpoint-free.",
		}
	}
}
