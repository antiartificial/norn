package pipeline

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/hub"
	"norn/v2/api/model"
	"norn/v2/api/saga"
)

// Preflight runs a read-only deploy rehearsal for an app.
// It validates the spec, prepares the same source tree deploy would use, builds
// the image locally, and runs build.test without snapshot, migration, submit, or
// forge side effects.
func (p *Pipeline) Preflight(spec *model.InfraSpec, ref string) string {
	sg := saga.New(p.SagaStore, spec.App, "pipeline", "preflight")
	ctx := context.Background()
	operationID := uuid.New().String()
	if err := p.DB.InsertOperation(ctx, &model.Operation{
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
	}); err != nil {
		log.Printf("preflight: insert operation: %v", err)
		operationID = ""
	}

	sg.Log(ctx, "preflight.queued", fmt.Sprintf("queued preflight for %s (ref: %s)", spec.App, ref), nil)
	return sg.ID
}

func truthyEnv(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (p *Pipeline) runPreflight(ctx context.Context, spec *model.InfraSpec, ref string, sg *saga.Saga, operationID string) {
	st := &state{
		spec:      spec,
		commitSHA: ref,
		sourceRef: ref,
		preflight: true,
	}
	defer func() {
		if st.workDir != "" {
			_ = os.RemoveAll(st.workDir)
		}
	}()

	steps := []step{
		{name: "validate", fn: p.preflightValidate},
		{name: "clone", fn: p.clone},
		{name: "inspect", fn: p.preflightInspect},
		{name: "build", fn: p.build},
		{name: "test", fn: p.test},
	}

	total := fmt.Sprintf("%d", len(steps))
	for i, s := range steps {
		idx := fmt.Sprintf("%d", i+1)
		sg.StepStart(ctx, s.name)
		p.broadcastPreflightStep(spec.App, sg.ID, s.name, "running", idx, total, 0)

		start := time.Now()
		err := s.fn(ctx, st, sg)
		elapsed := time.Since(start).Milliseconds()

		if err != nil {
			sg.StepFailed(ctx, s.name, err)
			p.broadcastPreflightStep(spec.App, sg.ID, s.name, "failed", idx, total, elapsed)
			sg.Log(ctx, "preflight.failed", fmt.Sprintf("preflight failed at %s: %v", s.name, err), nil)
			if operationID != "" {
				_ = p.DB.FinishOperation(ctx, operationID, model.OperationFailed, fmt.Sprintf("preflight failed at %s: %v", s.name, err), map[string]interface{}{
					"step": s.name,
				})
			}
			p.broadcastPreflightDone("preflight.failed", spec.App, sg.ID, map[string]string{"error": err.Error()})
			return
		}

		sg.StepComplete(ctx, s.name, elapsed)
		p.broadcastPreflightStep(spec.App, sg.ID, s.name, "complete", idx, total, elapsed)
	}

	sg.Log(ctx, "preflight.complete", fmt.Sprintf("preflight complete: %s -> %s", spec.App, st.imageTag), map[string]string{
		"commitSha":  st.commitSHA,
		"imageTag":   st.imageTag,
		"sourceKind": st.sourceKind,
		"sourceRef":  st.sourceRef,
	})
	if operationID != "" {
		_ = p.DB.FinishOperation(ctx, operationID, model.OperationSucceeded, fmt.Sprintf("preflight complete: %s", spec.App), map[string]interface{}{
			"commitSha": st.commitSHA,
			"imageTag":  st.imageTag,
		})
	}
	p.broadcastPreflightDone("preflight.completed", spec.App, sg.ID, map[string]string{
		"imageTag":  st.imageTag,
		"commitSha": st.commitSHA,
	})
}

func (p *Pipeline) preflightValidate(ctx context.Context, st *state, sg *saga.Saga) error {
	result := model.ValidateSpecWithOptions(st.spec, model.ValidationOptions{
		NetworkMode:   p.NetworkMode,
		StrictSecrets: truthyEnv("NORN_STRICT_SECRETS"),
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
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "go.mod" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.Contains(line, "=> ../") {
				refs = append(refs, rel+": "+line)
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
