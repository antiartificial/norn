package pipeline

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"norn/v2/api/saga"
)

func localGitChanges(ctx context.Context, srcDir string) (bool, []string) {
	cmd := exec.CommandContext(ctx, "git", "-C", srcDir, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return false, nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return false, nil
	}
	changes := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > 3 {
			line = line[3:]
		}
		changes = append(changes, line)
	}
	return len(changes) > 0, changes
}

func (p *Pipeline) recordSourceProvenance(ctx context.Context, st *state, sg *saga.Saga) {
	metadata := map[string]string{
		"kind":      st.sourceKind,
		"path":      st.sourcePath,
		"ref":       st.sourceRef,
		"commitSha": st.commitSHA,
		"dirty":     fmt.Sprintf("%t", st.sourceDirty),
	}
	if len(st.sourceChanges) > 0 {
		metadata["dirtyCount"] = fmt.Sprintf("%d", len(st.sourceChanges))
		metadata["dirtyFiles"] = strings.Join(limitStrings(st.sourceChanges, 12), ",")
	}
	message := fmt.Sprintf("source %s resolved to %s", st.sourceKind, st.commitSHA)
	if st.sourceDirty {
		message += fmt.Sprintf(" with %d dirty file(s)", len(st.sourceChanges))
	}
	_ = sg.Log(ctx, "source.provenance", message, metadata)
}

func limitStrings(values []string, limit int) []string {
	if len(values) <= limit {
		return values
	}
	return values[:limit]
}
