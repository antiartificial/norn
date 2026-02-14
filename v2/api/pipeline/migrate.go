package pipeline

import (
	"context"
	"fmt"
	"os/exec"

	"norn/v2/api/saga"
)

func (p *Pipeline) migrate(ctx context.Context, st *state, sg *saga.Saga) error {
	if st.spec.Migrations == "" {
		return nil // skip
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", st.spec.Migrations)
	if st.workDir != "" {
		cmd.Dir = st.workDir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("migration failed: %s", string(out))
	}
	return nil
}
