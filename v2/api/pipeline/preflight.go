package pipeline

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/effect"
	"norn/v2/api/hub"
	"norn/v2/api/model"
	"norn/v2/api/saga"
	"norn/v2/api/store"
)

// Preflight atomically accepts a read-only deploy rehearsal for an app.
// It validates the spec, prepares the same source tree deploy would use, builds
// the image locally, and runs build.test without snapshot, migration, submit, or
// forge side effects.
func (p *Pipeline) Preflight(ctx context.Context, spec *model.InfraSpec, ref string, request EnqueueRequest) (store.AcceptedOperation, error) {
	if p == nil || p.DB == nil || p.SagaStore == nil || spec == nil {
		return store.AcceptedOperation{}, fmt.Errorf("preflight pipeline is unavailable")
	}
	sg := saga.New(p.SagaStore, spec.App, "pipeline", "preflight")
	operationID := uuid.New().String()
	operation := model.Operation{
		ID:          operationID,
		Kind:        "app.preflight",
		App:         spec.App,
		SagaID:      sg.ID,
		Ref:         ref,
		Status:      model.OperationQueued,
		Risk:        "read-only",
		Source:      "pipeline",
		Message:     fmt.Sprintf("queued preflight for %s", spec.App),
		StartedAt:   time.Now(),
		MaxAttempts: 3,
		Payload: map[string]interface{}{
			"app": spec.App,
			"ref": ref,
		},
	}
	accepted, err := p.acceptOperation(ctx, request, operation, nil, nil)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	if !accepted.Replayed {
		sg.Log(ctx, "preflight.queued", fmt.Sprintf("queued preflight for %s (ref: %s)", spec.App, ref), map[string]string{"operationId": accepted.Operation.ID})
	}
	return accepted, nil
}

func truthyEnv(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (p *Pipeline) runPreflightWithArtifact(ctx context.Context, spec *model.InfraSpec, ref, artifact string, candidate model.ReleaseCandidate, sg *saga.Saga, claim store.OperationClaim) *OperationResult {
	st := &state{
		spec:          spec,
		commitSHA:     ref,
		sourceRef:     ref,
		preflight:     true,
		imageTag:      artifact,
		artifactBound: artifact != "",
		candidate:     candidate,
		claim:         claim,
	}
	keepWorkDir := false
	defer func() {
		// A deferred effect may still be running in this checkout.
		if st.workDir != "" && !keepWorkDir {
			_ = os.RemoveAll(st.workDir)
		}
	}()

	steps := []step{
		{name: "validate", fn: p.preflightValidate},
		{name: "clone", fn: p.checkpointedClone},
		{name: "admission", fn: p.admission},
		{name: "inspect", fn: p.preflightInspect},
		{name: "build", fn: p.build},
		{name: "artifact-admission", fn: p.artifactAdmission},
		{name: "test", fn: p.test},
	}

	total := fmt.Sprintf("%d", len(steps))
	for i, s := range steps {
		if err := ctx.Err(); err != nil {
			return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: fmt.Sprintf("preflight canceled before %s: %v", s.name, err), Metadata: map[string]interface{}{"step": s.name}}
		}
		idx := fmt.Sprintf("%d", i+1)
		sg.StepStart(ctx, s.name)
		p.broadcastPreflightStep(spec.App, sg.ID, s.name, "running", idx, total, 0)

		start := time.Now()
		err := runClaimedStep(ctx, func() error { return s.fn(ctx, st, sg) })
		elapsed := time.Since(start).Milliseconds()

		if err != nil && effect.IsDeferred(err) {
			keepWorkDir = true
			sg.Log(ctx, "preflight.step.pending", fmt.Sprintf("%s awaiting external effect recovery: %v", s.name, err), map[string]string{"step": s.name})
			return deferredResult(claim, err)
		}
		if err != nil {
			sg.StepFailed(ctx, s.name, err)
			p.broadcastPreflightStep(spec.App, sg.ID, s.name, "failed", idx, total, elapsed)
			stepName, stepErr := s.name, err
			message := fmt.Sprintf("preflight failed at %s: %v", stepName, stepErr)
			return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: message, Metadata: map[string]interface{}{"step": stepName}, publish: func(publishCtx context.Context) {
				sg.Log(publishCtx, "preflight.failed", message, nil)
				p.broadcastPreflightDone("preflight.failed", spec.App, sg.ID, map[string]string{"error": stepErr.Error()})
			}}
		}

		sg.StepComplete(ctx, s.name, elapsed)
		p.broadcastPreflightStep(spec.App, sg.ID, s.name, "complete", idx, total, elapsed)
	}

	return &OperationResult{Claim: claim, Status: model.OperationSucceeded, Message: fmt.Sprintf("preflight complete: %s", spec.App), Metadata: map[string]interface{}{"commitSha": st.commitSHA, "imageTag": st.imageTag}, publish: func(publishCtx context.Context) {
		sg.Log(publishCtx, "preflight.complete", fmt.Sprintf("preflight complete: %s -> %s", spec.App, st.imageTag), map[string]string{"commitSha": st.commitSHA, "imageTag": st.imageTag, "sourceKind": st.sourceKind, "sourceRef": st.sourceRef})
		p.broadcastPreflightDone("preflight.completed", spec.App, sg.ID, map[string]string{"imageTag": st.imageTag, "commitSha": st.commitSHA})
	}}
}

func (p *Pipeline) preflightValidate(ctx context.Context, st *state, sg *saga.Saga) error {
	result := model.ValidateSpecWithOptions(st.spec, model.ValidationOptions{
		NetworkMode:   p.NetworkMode,
		StrictSecrets: p.StrictSecrets || truthyEnv("NORN_STRICT_SECRETS"),
	})
	for _, finding := range result.Findings {
		p.preflightProgress(ctx, st.spec.App, sg, fmt.Sprintf("%s %s: %s", finding.Severity, finding.Field, finding.Message), map[string]string{
			"severity": finding.Severity,
			"field":    finding.Field,
			"step":     "validate",
		})
	}
	if result.Valid {
		return nil
	}

	var errors []string
	for _, finding := range result.Findings {
		if finding.Severity == "error" {
			errors = append(errors, fmt.Sprintf("%s: %s", finding.Field, finding.Message))
		}
	}
	if len(errors) == 0 {
		return fmt.Errorf("infraspec validation failed")
	}
	return fmt.Errorf("infraspec validation failed: %s", strings.Join(errors, "; "))
}

func (p *Pipeline) preflightInspect(ctx context.Context, st *state, sg *saga.Saga) error {
	if st.workDir == "" {
		return fmt.Errorf("source workdir is not prepared")
	}
	if _, err := os.Stat(filepath.Join(st.workDir, "infraspec.yaml")); err != nil {
		return fmt.Errorf("infraspec.yaml missing from prepared source: %w", err)
	}

	if st.spec.Build != nil {
		dockerfile := "Dockerfile"
		if st.spec.Build.Dockerfile != "" {
			dockerfile = st.spec.Build.Dockerfile
		}
		if _, err := os.Stat(filepath.Join(st.workDir, dockerfile)); err != nil {
			return fmt.Errorf("dockerfile %q missing from prepared source: %w", dockerfile, err)
		}
	}

	if err := p.checkDeclaredSecrets(st.spec); err != nil {
		return err
	}

	if st.spec.Repo != nil && !st.spec.Repo.AutoDeploy {
		p.preflightProgress(ctx, st.spec.App, sg, "repo.autoDeploy is false; webhook pushes will not deploy this app", map[string]string{
			"severity": "warning",
			"field":    "repo.autoDeploy",
			"step":     "inspect",
		})
	}

	for _, ref := range parentPathReferences(st.workDir) {
		p.preflightProgress(ctx, st.spec.App, sg, "parent-directory dependency reference: "+ref, map[string]string{
			"severity": "warning",
			"field":    "source.parentReference",
			"step":     "inspect",
		})
	}

	return nil
}

func (p *Pipeline) checkDeclaredSecrets(spec *model.InfraSpec) error {
	if len(spec.Secrets) == 0 {
		return nil
	}
	if p.Secrets == nil {
		return fmt.Errorf("secrets declared but no secrets manager is configured")
	}
	keys, err := p.Secrets.List(spec.App)
	if err != nil {
		return fmt.Errorf("declared secrets are not readable for %s: %w", spec.App, err)
	}
	have := map[string]bool{}
	for _, key := range keys {
		have[strings.TrimSpace(key)] = true
	}
	var missing []string
	for _, key := range spec.Secrets {
		key = strings.TrimSpace(key)
		if key != "" && !have[key] {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing encrypted secrets: %s", strings.Join(missing, ", "))
	}
	return nil
}

func parentPathReferences(root string) []string {
	var refs []string
	confined, err := os.OpenRoot(root)
	if err != nil {
		return refs
	}
	defer confined.Close()
	_ = fs.WalkDir(confined.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "go.mod" {
			return nil
		}
		data, err := fs.ReadFile(confined.FS(), path)
		if err != nil {
			return nil
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.Contains(line, "=> ../") {
				refs = append(refs, filepath.ToSlash(path)+": "+line)
			}
		}
		return nil
	})
	return refs
}

func (p *Pipeline) preflightProgress(ctx context.Context, app string, sg *saga.Saga, message string, metadata map[string]string) {
	_ = sg.Log(ctx, "preflight.progress", message, metadata)
	if p.WS == nil {
		return
	}
	payload := map[string]string{
		"sagaId":  sg.ID,
		"message": message,
	}
	for key, value := range metadata {
		payload[key] = value
	}
	p.WS.Broadcast(hub.Event{Type: "preflight.progress", AppID: app, Payload: payload})
}

func (p *Pipeline) broadcastPreflightStep(app, sagaID, stepName, status, idx, total string, durationMs int64) {
	if p.WS == nil {
		return
	}
	payload := map[string]string{
		"step":   stepName,
		"sagaId": sagaID,
		"status": status,
		"index":  idx,
		"total":  total,
	}
	if durationMs > 0 {
		payload["durationMs"] = fmt.Sprintf("%d", durationMs)
	}
	p.WS.Broadcast(hub.Event{Type: "preflight.step", AppID: app, Payload: payload})
}

func (p *Pipeline) broadcastPreflightDone(eventType, app, sagaID string, extra map[string]string) {
	if p.WS == nil {
		return
	}
	payload := map[string]string{"sagaId": sagaID}
	for key, value := range extra {
		payload[key] = value
	}
	p.WS.Broadcast(hub.Event{Type: eventType, AppID: app, Payload: payload})
}
