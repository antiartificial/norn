package cmd

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"norn/v2/cli/api"
	"norn/v2/cli/style"
)

var (
	secretsMigrateApply   bool
	secretsMigrateAppsDir string
)

func init() {
	secretsCmd.AddCommand(secretsMigrateCmd)
	secretsMigrateCmd.Flags().BoolVar(&secretsMigrateApply, "apply", false, "Modify infraspec files (remove plaintext env values, add to secrets list)")
	secretsMigrateCmd.Flags().StringVar(&secretsMigrateAppsDir, "apps-dir", "", "Directory containing app folders (default: $NORN_APPS_DIR or $HOME/projects)")
}

var secretsMigrateCmd = &cobra.Command{
	Use:   "migrate [app]",
	Short: "Migrate plaintext env secrets to encrypted secrets",
	Long: `Fetch the migration plan and generate SOPS commands to encrypt plaintext secrets.

By default (dry-run) this prints what will change and the SOPS commands to run.
With --apply it also modifies infraspec.yaml: removes keys from env: and adds
them to the secrets: list. The SOPS commands still need to be run manually.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := ""
		if len(args) == 1 {
			appID = args[0]
		}

		plan, err := client.SecretsMigrationPlan(appID)
		if err != nil {
			return fmt.Errorf("failed to fetch migration plan: %w", err)
		}

		appsDir := resolveAppsDir(secretsMigrateAppsDir)
		return runSecretsMigrate(plan, appsDir, secretsMigrateApply)
	},
}

func resolveAppsDir(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if v := os.Getenv("NORN_APPS_DIR"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, "projects")
}

// appMigrationGroup groups migration items by app.
type appMigrationGroup struct {
	app   string
	items []api.SecretMigrationItem
}

func runSecretsMigrate(plan *api.SecretMigrationPlan, appsDir string, apply bool) error {
	title := "secrets migrate"
	if plan.App != "" {
		title += " for " + plan.App
	}
	fmt.Println(style.Title.Render(title))
	fmt.Printf("generated=%s items=%d\n", plan.GeneratedAt, plan.Count)

	if len(plan.Items) == 0 {
		fmt.Println()
		fmt.Println(style.Healthy.Render("nothing to migrate — no plaintext secret-like env entries found"))
		return nil
	}

	mode := "dry-run"
	if apply {
		mode = "apply"
	}
	fmt.Printf("mode=%s apps_dir=%s\n\n", mode, appsDir)

	// Group items by app.
	grouped := groupByApp(plan.Items)

	var totalMigrated int
	var errors []string

	for _, group := range grouped {
		migrated, err := processMigrateGroup(group, appsDir, apply)
		totalMigrated += migrated
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", group.app, err))
		}
	}

	fmt.Println()
	if len(errors) > 0 {
		fmt.Println(style.Warning.Render("completed with errors:"))
		for _, e := range errors {
			fmt.Printf("  %s %s\n", style.StepFailed.Render("✗"), e)
		}
		return fmt.Errorf("%d error(s) during migration", len(errors))
	}

	if apply {
		fmt.Println(style.SuccessBox.Render(fmt.Sprintf("infraspecs updated: %d key(s) moved to secrets list", totalMigrated)))
		fmt.Println()
		fmt.Println(style.Bold.Render("Next step: run the SOPS commands above to encrypt the values."))
	} else {
		fmt.Println(style.DimText.Render("dry-run complete — run with --apply to modify infraspec files"))
	}

	return nil
}

func groupByApp(items []api.SecretMigrationItem) []appMigrationGroup {
	seen := map[string]int{}
	var groups []appMigrationGroup
	for _, item := range items {
		idx, ok := seen[item.App]
		if !ok {
			idx = len(groups)
			seen[item.App] = idx
			groups = append(groups, appMigrationGroup{app: item.App})
		}
		groups[idx].items = append(groups[idx].items, item)
	}
	return groups
}

func processMigrateGroup(group appMigrationGroup, appsDir string, apply bool) (int, error) {
	appDir := filepath.Join(appsDir, group.app)
	infraspecPath := filepath.Join(appDir, "infraspec.yaml")
	encSecretsPath := filepath.Join(appDir, "secrets.enc.yaml")

	fmt.Printf("%s %s\n", style.Bold.Render("app"), group.app)
	fmt.Printf("  infraspec: %s\n", infraspecPath)

	// Only items that are in a plaintext env field and need encryption.
	var toMigrate []api.SecretMigrationItem
	for _, item := range group.items {
		if strings.HasPrefix(item.Field, "env") || strings.Contains(strings.ToLower(item.Action), "encrypt") {
			toMigrate = append(toMigrate, item)
		}
	}

	if len(toMigrate) == 0 {
		fmt.Printf("  %s\n\n", style.DimText.Render("no env-field items to migrate for this app"))
		return 0, nil
	}

	// Print per-key actions.
	for _, item := range toMigrate {
		status := style.Warning.Render("•")
		fmt.Printf("  %s %s  (%s)\n", status, item.Key, item.Action)
	}
	fmt.Println()

	// Generate SOPS commands.
	fmt.Println(style.Bold.Render("  SOPS commands to run:"))
	fmt.Println()

	encSecretsExists := fileExists(encSecretsPath)
	if !encSecretsExists {
		fmt.Printf("  # Create the encrypted secrets file (run once)\n")
		fmt.Printf("  sops --encrypt --age $(cat ~/.config/sops/age/keys.txt | grep 'public key:' | awk '{print $NF}') \\\n")
		fmt.Printf("    --encrypted-regex '^.*$' \\\n")
		fmt.Printf("    --input-type yaml --output-type yaml \\\n")
		fmt.Printf("    /dev/stdin > %s <<EOF\n", encSecretsPath)
		fmt.Println("  # placeholder: replace with actual values")
		for _, item := range toMigrate {
			fmt.Printf("  %s: \"<value>\"\n", item.Key)
		}
		fmt.Println("  EOF")
		fmt.Println()
		fmt.Printf("  # Or use sops directly to create and edit:\n")
		fmt.Printf("  sops %s\n", encSecretsPath)
		fmt.Println()
	}

	for _, item := range toMigrate {
		if encSecretsExists {
			fmt.Printf("  # Add %s to existing encrypted file\n", item.Key)
			fmt.Printf("  sops --set '[%q] \"<value>\"' %s\n", item.Key, encSecretsPath)
		} else {
			fmt.Printf("  # After creating the file above, set %s:\n", item.Key)
			fmt.Printf("  sops --set '[%q] \"<value>\"' %s\n", item.Key, encSecretsPath)
		}
	}
	fmt.Println()

	if !apply {
		fmt.Printf("  %s infraspec changes not written (dry-run)\n\n", style.DimText.Render("→"))
		return 0, nil
	}

	// Apply: read and modify the infraspec.
	content, err := os.ReadFile(infraspecPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("  %s infraspec not found at %s — skipping\n\n", style.Warning.Render("!"), infraspecPath)
			return 0, nil
		}
		return 0, fmt.Errorf("read infraspec: %w", err)
	}

	keys := make([]string, 0, len(toMigrate))
	for _, item := range toMigrate {
		keys = append(keys, item.Key)
	}

	modified, err := modifyInfraspec(content, keys)
	if err != nil {
		return 0, fmt.Errorf("modify infraspec: %w", err)
	}

	if err := os.WriteFile(infraspecPath, modified, 0o644); err != nil {
		return 0, fmt.Errorf("write infraspec: %w", err)
	}

	fmt.Printf("  %s infraspec updated — %d key(s) removed from env, added to secrets list\n\n",
		style.StepDone.Render("✓"), len(keys))

	return len(keys), nil
}

// modifyInfraspec removes the given keys from the env: block and adds them
// to the secrets: list. It preserves all other formatting and comments.
func modifyInfraspec(content []byte, keys []string) ([]byte, error) {
	keySet := make(map[string]bool, len(keys))
	for _, k := range keys {
		keySet[k] = true
	}

	lines := splitLines(content)

	// Pass 1: remove matching lines from env: block and collect existing secrets.
	inEnvBlock := false
	inSecretsBlock := false
	existingSecrets := map[string]bool{}
	var kept []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Detect top-level block transitions (lines starting without indentation
		// that end with ':' or are a key at indent=0).
		if len(line) > 0 && line[0] != ' ' && line[0] != '\t' && line[0] != '#' && line[0] != '-' {
			// Top-level key
			inEnvBlock = strings.HasPrefix(trimmed, "env:")
			inSecretsBlock = strings.HasPrefix(trimmed, "secrets:")
		}

		if inSecretsBlock && strings.HasPrefix(trimmed, "- ") {
			secretKey := strings.TrimPrefix(trimmed, "- ")
			existingSecrets[strings.TrimSpace(secretKey)] = true
		}

		if inEnvBlock && !strings.HasPrefix(trimmed, "env:") {
			// Check if this line is one of the keys to remove.
			// Lines look like "  KEY: value" or "  KEY: 'value'"
			colonIdx := strings.Index(trimmed, ":")
			if colonIdx > 0 {
				lineKey := strings.TrimSpace(trimmed[:colonIdx])
				if keySet[lineKey] {
					// Skip this line (remove it).
					continue
				}
			}
		}

		kept = append(kept, line)
	}

	// Pass 2: add missing keys to secrets: list.
	// Find the secrets: block and append to it, or insert a new secrets: block
	// before the processes: block (or at end of preamble if no processes:).
	var keysToAdd []string
	for _, k := range keys {
		if !existingSecrets[k] {
			keysToAdd = append(keysToAdd, k)
		}
	}

	if len(keysToAdd) == 0 {
		return joinLines(kept), nil
	}

	// Try to find the secrets: block to append to.
	// secretsBlockEnd tracks the last non-blank line inside the secrets block.
	secretsBlockEnd := -1
	inSBlock := false
	for i, line := range kept {
		trimmed := strings.TrimSpace(line)
		if len(line) > 0 && line[0] != ' ' && line[0] != '\t' && line[0] != '#' && line[0] != '-' {
			if strings.HasPrefix(trimmed, "secrets:") {
				inSBlock = true
				secretsBlockEnd = i
				continue
			}
			if inSBlock {
				// Hit next top-level key — secrets block ended before this line.
				inSBlock = false
			}
		}
		if inSBlock && trimmed != "" {
			// Only advance end pointer on non-blank lines so we insert
			// right after the last list item, not after a trailing blank line.
			secretsBlockEnd = i
		}
	}

	if secretsBlockEnd >= 0 {
		// Insert new keys after the last line of the secrets block.
		var newLines []string
		for _, k := range keysToAdd {
			newLines = append(newLines, "  - "+k)
		}
		result := make([]string, 0, len(kept)+len(newLines))
		result = append(result, kept[:secretsBlockEnd+1]...)
		result = append(result, newLines...)
		result = append(result, kept[secretsBlockEnd+1:]...)
		return joinLines(result), nil
	}

	// No secrets: block exists — insert one before processes: or before env:
	// or at the top after the name: line.
	insertAt := 1 // default: after first line
	for i, line := range kept {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "processes:") || strings.HasPrefix(trimmed, "env:") {
			insertAt = i
			break
		}
	}

	var newBlock []string
	// Only prepend a blank separator if the line just before insertAt is non-blank.
	if insertAt > 0 && strings.TrimSpace(kept[insertAt-1]) != "" {
		newBlock = append(newBlock, "")
	}
	newBlock = append(newBlock, "secrets:")
	for _, k := range keysToAdd {
		newBlock = append(newBlock, "  - "+k)
	}
	// Add a blank line after the new block if the line at insertAt is non-blank.
	if insertAt < len(kept) && strings.TrimSpace(kept[insertAt]) != "" {
		newBlock = append(newBlock, "")
	}

	result := make([]string, 0, len(kept)+len(newBlock))
	result = append(result, kept[:insertAt]...)
	result = append(result, newBlock...)
	result = append(result, kept[insertAt:]...)
	return joinLines(result), nil
}

// splitLines splits content into lines preserving trailing newline behaviour.
func splitLines(content []byte) []string {
	var lines []string
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines
}

// joinLines joins lines back with newlines, adding a trailing newline.
func joinLines(lines []string) []byte {
	return []byte(strings.Join(lines, "\n") + "\n")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
