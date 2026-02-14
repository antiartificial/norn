package pipeline

import (
	"context"
	"fmt"
	"os/exec"

	"norn/v2/api/saga"
)

func (p *Pipeline) test(ctx context.Context, st *state, sg *saga.Saga) error {
	if st.spec.Build == nil || st.spec.Build.Test == "" {
		return nil // skip
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", st.spec.Build.Test)
	cmd.Dir = st.workDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tests failed: %s", string(out))
	}
	return nil
}
