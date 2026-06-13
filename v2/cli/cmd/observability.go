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
var observabilityInstallOverwrite bool

func init() {
	rootCmd.AddCommand(observabilityCmd)
	observabilityCmd.AddCommand(observabilityBundleCmd)
	observabilityCmd.AddCommand(observabilityInstallCmd)
	observabilityBundleCmd.Flags().StringVar(&observabilityOutDir, "out", "", "Write bundle files to this directory")
	observabilityInstallCmd.Flags().BoolVar(&observabilityInstallOverwrite, "overwrite", false, "Replace generated observability app files if they already exist")
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

var observabilityInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install generated observability service apps into NORN_APPS_DIR",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		receipt, err := client.InstallObservabilityServices(observabilityInstallOverwrite)
		if err != nil {
			return err
		}
		fmt.Println(style.SuccessBox.Render("observability services installed"))
		fmt.Printf("apps_dir=%s status=%s\n", receipt.AppsDir, receipt.Status)
		for _, app := range receipt.Installed {
			fmt.Printf("  app %s\n", app)
		}
		if len(receipt.Files) > 0 {
			fmt.Println()
			fmt.Println(style.Subtitle.Render("  files"))
			for _, file := range receipt.Files {
				fmt.Printf("  %s\n", file)
			}
		}
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
