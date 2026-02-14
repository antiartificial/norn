package pipeline

import (
	"context"
	"os"

	"norn/v2/api/saga"
)

func (p *Pipeline) cleanup(ctx context.Context, st *state, sg *saga.Saga) error {
	if st.workDir == "" {
		return nil
	}
	return os.RemoveAll(st.workDir)
}
