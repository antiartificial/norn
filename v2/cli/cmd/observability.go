package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"norn/v2/cli/api"
	"norn/v2/cli/style"
)

var observabilityOutDir string

func init() {
	rootCmd.AddCommand(observabilityCmd)
	observabilityCmd.AddCommand(observabilityBundleCmd)
	observabilityBundleCmd.Flags().StringVar(&observabilityOutDir, "out", "", "Write bundle files to this directory")
}

var observabilityCmd = &cobra.Command{
	Use:   "observability",
	Short: "Generate Norn Prometheus and Grafana observability assets",
}

var observabilityBundleCmd = &cobra.Command{
	Use:   "bundle",
	Short: "Show or write the Norn observability bundle",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		bundle, err := client.ObservabilityBundle()
		if err != nil {
			return err
		}
		if observabilityOutDir != "" {
			return writeObservabilityBundle(observabilityOutDir, bundle)
		}
		printObservabilityBundle(bundle)
		return nil
	},
}

func printObservabilityBundle(bundle *api.ObservabilityBundle) {
	fmt.Println(style.Title.Render("norn observability bundle"))
	fmt.Printf("generated=%s retention=%s\n", bundle.GeneratedAt, bundle.Retention)
	fmt.Printf("prometheus=%d bytes alerts=%d bytes grafana_dashboard=%d bytes\n",
		len(bundle.PrometheusConfig),
		len(bundle.AlertRules),
		len(bundle.GrafanaDashboard),
	)
	keys := make([]string, 0, len(bundle.ServiceSpecs))
	for key := range bundle.ServiceSpecs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	fmt.Println()
	fmt.Println(style.Subtitle.Render("  service specs"))
	for _, key := range keys {
		fmt.Printf("  %s %d bytes\n", key, len(bundle.ServiceSpecs[key]))
	}
}

func writeObservabilityBundle(dir string, bundle *api.ObservabilityBundle) error {
	files := map[string]string{
		"prometheus/prometheus.yml":                             bundle.PrometheusConfig,
		"prometheus/rules/norn-alerts.yml":                      bundle.AlertRules,
		"grafana/provisioning/datasources/norn-prometheus.json": bundle.GrafanaDatasource,
		"grafana/dashboards/norn-platform.json":                 bundle.GrafanaDashboard,
	}
	for name, content := range bundle.ServiceSpecs {
		files[filepath.Join("services", name+".infraspec.yaml")] = content
	}
	for rel, content := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
	}
	fmt.Println(style.SuccessBox.Render("observability bundle written"))
	fmt.Println(dir)
	return nil
}
