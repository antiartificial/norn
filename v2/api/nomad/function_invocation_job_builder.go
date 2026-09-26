package nomad

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"
	"time"

	nomadapi "github.com/hashicorp/nomad/api"
)

// FunctionInvocationJobDialectVersion changes whenever the closed job shape or
// its projection changes. It is part of the digest preimage.
const FunctionInvocationJobDialectVersion = "norn.function-invocation.nomad/v4"

const (
	functionInvocationGroupName = "invoke"
	functionInvocationTaskName  = "invoke"
	functionInvocationOwnerMeta = "norn.function-invocation.owner"
	functionInvocationFilesMeta = "norn.function-invocation.files"
	functionInvocationTemplate  = "secrets/norn-function-invocation"
	functionInvocationFilesDir  = "norn-function-databases"
)

var functionInvocationImageReference = regexp.MustCompile(`^[^\s@]+@sha256:[0-9a-f]{64}$`)

var (
	ErrFunctionInvocationJobRequest = errors.New("function invocation job request is invalid")
	ErrFunctionInvocationJobDialect = errors.New("function invocation job is outside the closed dialect")
)

// FunctionInvocationJobRequest contains the complete public workload shape.
// It deliberately has no Env, Config, or private-material field. Private
// invocation bytes are delivered only through VariablePath at runtime.
type FunctionInvocationJobRequest struct {
	JobID        string
	OwnerMarker  string
	VariablePath string
	Image        string
	Command      string
	CPU          int
	MemoryMB     int
	Files        []FunctionInvocationFileLayout
}

// FunctionInvocationFileLayout is the public shape of one private runtime
// file. Key selects bytes from the opaque invocation payload; Env receives
// only the allocation-local path. Neither field contains private material.
type FunctionInvocationFileLayout struct {
	Key string `json:"key"`
	Env string `json:"env"`
}

// FunctionInvocationJobDigest is safe to persist and compare with a Nomad
// read-back. The canonical bytes never include private variable content.
type FunctionInvocationJobDigest struct {
	Version string
	Digest  string
}

// BuildFunctionInvocationJob creates the only function-invocation Nomad job
// dialect. It is pure and never uses TranslateBatchAt or application Env.
func BuildFunctionInvocationJob(request FunctionInvocationJobRequest) (*nomadapi.Job, FunctionInvocationJobDigest, error) {
	if err := validateFunctionInvocationJobRequest(request); err != nil {
		return nil, FunctionInvocationJobDigest{}, err
	}
	jobID, groupName, taskName := request.JobID, functionInvocationGroupName, functionInvocationTaskName
	region, namespace, jobType := "global", "default", "batch"
	priority, count := 50, 1
	allAtOnce, stopped := false, false
	restartAttempts, cpuCores := 0, 0
	restartInterval, restartDelay := 24*time.Hour, 15*time.Second
	restartMode := "fail"
	renderTemplates := false
	rescheduleAttempts := 0
	rescheduleInterval, rescheduleDelay, rescheduleMaxDelay := time.Hour, 5*time.Second, 5*time.Second
	rescheduleDelayFunction, rescheduleUnlimited := "constant", false
	diskSize := 300
	diskSticky, diskMigrate := false, false
	maxFiles, maxFileSize, logsDisabled := TaskLogMaxFiles, TaskLogMaxFileSizeMB, false
	filesJSON, _ := json.Marshal(request.Files)
	templateData := functionInvocationEnvironmentTemplate(request.VariablePath, request.Files)
	templateSource, templateDestination := "", functionInvocationTemplate
	templateMode, templateSignal, templatePerms := "restart", "", "0400"
	templateOnce, templateEnv, templateErrMissing := false, true, true
	templateSplay, templateVaultGrace := 5*time.Second, time.Duration(0)
	templateLeft, templateRight := "{{", "}}"

	job := &nomadapi.Job{
		ID: &jobID, Name: &jobID, Type: &jobType, Region: &region, Namespace: &namespace,
		Priority: &priority, AllAtOnce: &allAtOnce, Stop: &stopped, Datacenters: []string{"dc1"},
		Meta: map[string]string{functionInvocationOwnerMeta: request.OwnerMarker, functionInvocationFilesMeta: string(filesJSON)},
	}
	group := &nomadapi.TaskGroup{
		Name: &groupName, Count: &count,
		RestartPolicy:    &nomadapi.RestartPolicy{Attempts: &restartAttempts, Interval: &restartInterval, Delay: &restartDelay, Mode: &restartMode, RenderTemplates: &renderTemplates},
		ReschedulePolicy: &nomadapi.ReschedulePolicy{Attempts: &rescheduleAttempts, Interval: &rescheduleInterval, Delay: &rescheduleDelay, DelayFunction: &rescheduleDelayFunction, MaxDelay: &rescheduleMaxDelay, Unlimited: &rescheduleUnlimited},
		EphemeralDisk:    &nomadapi.EphemeralDisk{SizeMB: &diskSize, Sticky: &diskSticky, Migrate: &diskMigrate},
	}
	task := &nomadapi.Task{
		Name: taskName, Driver: "docker",
		Config:    map[string]interface{}{"image": request.Image, "command": "/bin/sh", "args": []string{"-c", request.Command}},
		Resources: &nomadapi.Resources{CPU: &request.CPU, Cores: &cpuCores, MemoryMB: &request.MemoryMB, DiskMB: &diskSize},
		LogConfig: &nomadapi.LogConfig{MaxFiles: &maxFiles, MaxFileSizeMB: &maxFileSize, Disabled: &logsDisabled},
		Templates: []*nomadapi.Template{{SourcePath: &templateSource, DestPath: &templateDestination, EmbeddedTmpl: &templateData, ChangeMode: &templateMode, ChangeSignal: &templateSignal, Once: &templateOnce, Splay: &templateSplay, Perms: &templatePerms, LeftDelim: &templateLeft, RightDelim: &templateRight, Envvars: &templateEnv, VaultGrace: &templateVaultGrace, ErrMissingKey: &templateErrMissing}},
	}
	for _, file := range request.Files {
		data, destination, env := functionInvocationFileTemplate(request.VariablePath, file), functionInvocationFileDestination(file.Key), false
		task.Templates = append(task.Templates, &nomadapi.Template{SourcePath: &templateSource, DestPath: &destination, EmbeddedTmpl: &data, ChangeMode: &templateMode, ChangeSignal: &templateSignal, Once: &templateOnce, Splay: &templateSplay, Perms: &templatePerms, LeftDelim: &templateLeft, RightDelim: &templateRight, Envvars: &env, VaultGrace: &templateVaultGrace, ErrMissingKey: &templateErrMissing})
	}
	group.Tasks = []*nomadapi.Task{task}
	job.TaskGroups = []*nomadapi.TaskGroup{group}
	job.Canonicalize()
	digest, err := ProjectFunctionInvocationJob(job)
	if err != nil {
		return nil, FunctionInvocationJobDigest{}, err
	}
	return job, digest, nil
}

func validateFunctionInvocationJobRequest(request FunctionInvocationJobRequest) error {
	if !functionInvocationJobID.MatchString(request.JobID) || request.VariablePath != "nomad/jobs/"+request.JobID+"/invoke" || !functionInvocationImageReference.MatchString(request.Image) || strings.TrimSpace(request.OwnerMarker) == "" || strings.TrimSpace(request.Command) == "" || request.CPU < 1 || request.MemoryMB < 10 || strings.ContainsAny(request.OwnerMarker+request.Command, "\r\n\x00") || !validFunctionInvocationFiles(request.Files) {
		return ErrFunctionInvocationJobRequest
	}
	return nil
}

type functionInvocationJobProjection struct {
	Version      string                         `json:"version"`
	JobID        string                         `json:"jobId"`
	OwnerMarker  string                         `json:"ownerMarker"`
	VariablePath string                         `json:"variablePath"`
	Image        string                         `json:"image"`
	Command      string                         `json:"command"`
	CPU          int                            `json:"cpu"`
	MemoryMB     int                            `json:"memoryMb"`
	Files        []FunctionInvocationFileLayout `json:"files"`
}

// ProjectFunctionInvocationJob accepts only the v1 closed dialect. It checks
// every mutable workload surface that this dialect leaves unsupported, rather
// than silently dropping it while creating a digest.
func ProjectFunctionInvocationJob(job *nomadapi.Job) (FunctionInvocationJobDigest, error) {
	p, err := projectFunctionInvocationJob(job)
	if err != nil {
		return FunctionInvocationJobDigest{}, err
	}
	// projectFunctionInvocationJob verifies every fixed part of the dialect
	// before this compact public form is encoded. It therefore excludes only
	// server bookkeeping and static defaults, never unvalidated workload data.
	bytes, err := json.Marshal(p)
	if err != nil {
		return FunctionInvocationJobDigest{}, fmt.Errorf("%w: encode projection", ErrFunctionInvocationJobDialect)
	}
	sum := sha256.Sum256(bytes)
	return FunctionInvocationJobDigest{Version: FunctionInvocationJobDialectVersion, Digest: "sha256:" + hex.EncodeToString(sum[:])}, nil
}

func projectFunctionInvocationJob(job *nomadapi.Job) (functionInvocationJobProjection, error) {
	if job == nil {
		return functionInvocationJobProjection{}, functionInvocationDialectError("job")
	}
	if !equalString(job.ID, job.Name) {
		return functionInvocationJobProjection{}, functionInvocationDialectError("job.id-name")
	}
	if !equalStringValue(job.Type, "batch") {
		return functionInvocationJobProjection{}, functionInvocationDialectError("job.type")
	}
	if !equalStringValue(job.Region, "global") {
		return functionInvocationJobProjection{}, functionInvocationDialectError("job.region")
	}
	if !equalStringValue(job.Namespace, "default") {
		return functionInvocationJobProjection{}, functionInvocationDialectError("job.namespace")
	}
	if !equalIntValue(job.Priority, 50) {
		return functionInvocationJobProjection{}, functionInvocationDialectError("job.priority")
	}
	if !equalBoolValue(job.AllAtOnce, false) {
		return functionInvocationJobProjection{}, functionInvocationDialectError("job.all-at-once")
	}
	if !equalBoolValue(job.Stop, false) {
		return functionInvocationJobProjection{}, functionInvocationDialectError("job.stop")
	}
	if !reflect.DeepEqual(job.Datacenters, []string{"dc1"}) {
		return functionInvocationJobProjection{}, functionInvocationDialectError("job.datacenters")
	}
	if len(job.TaskGroups) != 1 {
		return functionInvocationJobProjection{}, functionInvocationDialectError("job.task-groups")
	}
	if field := unsupportedJobField(job); field != "" {
		return functionInvocationJobProjection{}, functionInvocationDialectError("job." + field)
	}
	group := job.TaskGroups[0]
	if group == nil {
		return functionInvocationJobProjection{}, functionInvocationDialectError("group")
	}
	if !equalStringValue(group.Name, functionInvocationGroupName) {
		return functionInvocationJobProjection{}, functionInvocationDialectError("group.name")
	}
	if !equalIntValue(group.Count, 1) {
		return functionInvocationJobProjection{}, functionInvocationDialectError("group.count")
	}
	if len(group.Tasks) != 1 {
		return functionInvocationJobProjection{}, functionInvocationDialectError("group.tasks")
	}
	if unsupportedGroup(group) {
		return functionInvocationJobProjection{}, functionInvocationDialectError("group.unsupported")
	}
	if !matchesRestart(group.RestartPolicy) {
		return functionInvocationJobProjection{}, functionInvocationDialectError("group.restart")
	}
	if !matchesReschedule(group.ReschedulePolicy) {
		return functionInvocationJobProjection{}, functionInvocationDialectError("group.reschedule")
	}
	if !matchesEphemeralDisk(group.EphemeralDisk) {
		return functionInvocationJobProjection{}, functionInvocationDialectError("group.ephemeral-disk")
	}
	task := group.Tasks[0]
	if task == nil {
		return functionInvocationJobProjection{}, functionInvocationDialectError("task")
	}
	if task.Name != functionInvocationTaskName {
		return functionInvocationJobProjection{}, functionInvocationDialectError("task.name")
	}
	if task.Driver != "docker" {
		return functionInvocationJobProjection{}, functionInvocationDialectError("task.driver")
	}
	if unsupportedTask(task) {
		return functionInvocationJobProjection{}, functionInvocationDialectError("task.unsupported")
	}
	if !matchesResources(task.Resources) {
		return functionInvocationJobProjection{}, functionInvocationDialectError("task.resources")
	}
	if !matchesLogConfig(task.LogConfig) {
		return functionInvocationJobProjection{}, functionInvocationDialectError("task.logs")
	}
	files, ok := functionInvocationFiles(job.Meta[functionInvocationFilesMeta])
	if !ok || len(task.Templates) != 1+len(files) {
		return functionInvocationJobProjection{}, functionInvocationDialectError("task.templates")
	}
	image, command, ok := functionInvocationTaskConfig(task.Config)
	if !ok || !functionInvocationImageReference.MatchString(image) {
		return functionInvocationJobProjection{}, functionInvocationDialectError("task.config")
	}
	variablePath, ok := functionInvocationTemplatePath(task.Templates[0], files)
	if !ok || variablePath != "nomad/jobs/"+*job.ID+"/invoke" || !equalMap(job.Meta, map[string]string{functionInvocationOwnerMeta: job.Meta[functionInvocationOwnerMeta], functionInvocationFilesMeta: job.Meta[functionInvocationFilesMeta]}) || strings.TrimSpace(job.Meta[functionInvocationOwnerMeta]) == "" {
		return functionInvocationJobProjection{}, functionInvocationDialectError("task.template")
	}
	for index, file := range files {
		if !matchesFunctionInvocationFileTemplate(task.Templates[index+1], variablePath, file) {
			return functionInvocationJobProjection{}, functionInvocationDialectError("task.file-template")
		}
	}
	return functionInvocationJobProjection{Version: FunctionInvocationJobDialectVersion, JobID: *job.ID, OwnerMarker: job.Meta[functionInvocationOwnerMeta], VariablePath: variablePath, Image: image, Command: command, CPU: *task.Resources.CPU, MemoryMB: *task.Resources.MemoryMB, Files: files}, nil
}

func functionInvocationDialectError(field string) error {
	return fmt.Errorf("%w: %s", ErrFunctionInvocationJobDialect, field)
}

func unsupportedJobField(j *nomadapi.Job) string {
	switch {
	case len(j.Constraints) != 0:
		return "constraints"
	case len(j.Affinities) != 0:
		return "affinities"
	case !defaultUpdate(j.Update):
		return "update"
	case j.Multiregion != nil:
		return "multiregion"
	case len(j.Spreads) != 0:
		return "spreads"
	case j.Periodic != nil:
		return "periodic"
	case j.ParameterizedJob != nil:
		return "parameterized"
	case j.Reschedule != nil:
		return "reschedule"
	case j.Migrate != nil:
		return "migrate"
	case j.UI != nil:
		return "ui"
	case j.NodePool != nil && *j.NodePool != "" && *j.NodePool != "default":
		return "node-pool"
	case j.Dispatched:
		return "dispatched"
	case len(j.Payload) != 0:
		return "payload"
	}
	return ""
}
func unsupportedGroup(g *nomadapi.TaskGroup) bool {
	return len(g.Constraints) != 0 || len(g.Affinities) != 0 || len(g.Spreads) != 0 || len(g.Volumes) != 0 || g.Disconnect != nil || g.Update != nil || g.Migrate != nil || len(g.Networks) != 0 || len(g.Meta) != 0 || len(g.Services) != 0 || g.ShutdownDelay != nil || g.StopAfterClientDisconnect != nil || g.MaxClientDisconnect != nil || g.Scaling != nil || g.Consul != nil || g.PreventRescheduleOnLost != nil && !equalBoolValue(g.PreventRescheduleOnLost, false)
}
func unsupportedTask(t *nomadapi.Task) bool {
	return t.User != "" || t.Lifecycle != nil || len(t.Constraints) != 0 || len(t.Affinities) != 0 || len(t.Env) != 0 || len(t.Services) != 0 || t.RestartPolicy != nil && !matchesRestart(t.RestartPolicy) || len(t.Meta) != 0 || t.KillTimeout == nil || *t.KillTimeout != 5*time.Second || len(t.Artifacts) != 0 || t.Vault != nil || t.Consul != nil || t.DispatchPayload != nil || len(t.VolumeMounts) != 0 || t.CSIPluginConfig != nil || t.Leader || t.ShutdownDelay != 0 || t.KillSignal != "" || t.Kind != "" || len(t.ScalingPolicies) != 0 || len(t.Secrets) != 0 || !defaultIdentity(t.Identity) || len(t.Identities) != 0 || len(t.Actions) != 0 || t.Schedule != nil
}

func matchesRestart(p *nomadapi.RestartPolicy) bool {
	return p != nil && equalIntValue(p.Attempts, 0) && equalDuration(p.Interval, 24*time.Hour) && equalDuration(p.Delay, 15*time.Second) && equalStringValue(p.Mode, "fail") && equalBoolValue(p.RenderTemplates, false)
}
func matchesReschedule(p *nomadapi.ReschedulePolicy) bool {
	return p != nil && equalIntValue(p.Attempts, 0) && equalDuration(p.Interval, time.Hour) && equalDuration(p.Delay, 5*time.Second) && equalStringValue(p.DelayFunction, "constant") && equalDuration(p.MaxDelay, 5*time.Second) && equalBoolValue(p.Unlimited, false)
}
func matchesEphemeralDisk(d *nomadapi.EphemeralDisk) bool {
	return d != nil && equalIntValue(d.SizeMB, 300) && equalBoolValue(d.Sticky, false) && equalBoolValue(d.Migrate, false)
}
func matchesResources(r *nomadapi.Resources) bool {
	return r != nil && r.CPU != nil && *r.CPU >= 1 && equalIntValue(r.Cores, 0) && r.MemoryMB != nil && *r.MemoryMB >= 10 && (equalIntValue(r.DiskMB, 300) || equalIntValue(r.DiskMB, 0)) && (r.MemoryMaxMB == nil || equalIntValue(r.MemoryMaxMB, 0)) && len(r.Networks) == 0 && len(r.Devices) == 0 && r.NUMA == nil && (r.SecretsMB == nil || equalIntValue(r.SecretsMB, 0)) && (r.IOPS == nil || equalIntValue(r.IOPS, 0))
}
func matchesLogConfig(l *nomadapi.LogConfig) bool {
	return l != nil && equalIntValue(l.MaxFiles, TaskLogMaxFiles) && equalIntValue(l.MaxFileSizeMB, TaskLogMaxFileSizeMB) && l.Enabled == nil && equalBoolValue(l.Disabled, false)
}

func functionInvocationTaskConfig(c map[string]interface{}) (string, string, bool) {
	if len(c) != 3 {
		return "", "", false
	}
	image, imageOK := c["image"].(string)
	shell, shellOK := c["command"].(string)
	args, argsOK := stringSlice(c["args"])
	if !imageOK || !shellOK || !argsOK || shell != "/bin/sh" || len(args) != 2 || args[0] != "-c" || strings.TrimSpace(args[1]) == "" || strings.ContainsAny(args[1], "\r\n\x00") {
		return "", "", false
	}
	return image, args[1], true
}
func stringSlice(value interface{}) ([]string, bool) {
	if values, ok := value.([]string); ok {
		return values, true
	}
	values, ok := value.([]interface{})
	if !ok {
		return nil, false
	}
	out := make([]string, len(values))
	for i, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil, false
		}
		out[i] = text
	}
	return out, true
}

func functionInvocationTemplatePath(t *nomadapi.Template, files []FunctionInvocationFileLayout) (string, bool) {
	if t == nil || !equalStringValue(t.SourcePath, "") || !equalStringValue(t.DestPath, functionInvocationTemplate) || t.EmbeddedTmpl == nil || !equalStringValue(t.ChangeMode, "restart") || !equalStringValue(t.ChangeSignal, "") || t.ChangeScript != nil || !equalBoolValue(t.Once, false) || !equalDuration(t.Splay, 5*time.Second) || !equalStringValue(t.Perms, "0400") || t.Uid != nil || t.Gid != nil || !equalStringValue(t.LeftDelim, "{{") || !equalStringValue(t.RightDelim, "}}") || !equalBoolValue(t.Envvars, true) || !equalDuration(t.VaultGrace, 0) || t.Wait != nil || !equalBoolValue(t.ErrMissingKey, true) {
		return "", false
	}
	const prefix = "{{ with nomadVar \""
	suffix := "\" }}" + functionInvocationEnvironmentTemplateSuffix(files)
	if !strings.HasPrefix(*t.EmbeddedTmpl, prefix) || !strings.HasSuffix(*t.EmbeddedTmpl, suffix) {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimPrefix(*t.EmbeddedTmpl, prefix), suffix), true
}

func functionInvocationEnvironmentTemplate(path string, files []FunctionInvocationFileLayout) string {
	return fmt.Sprintf("{{ with nomadVar %q }}%s", path, functionInvocationEnvironmentTemplateSuffix(files))
}

func functionInvocationEnvironmentTemplateSuffix(files []FunctionInvocationFileLayout) string {
	out := fmt.Sprintf("{{ $p := .%s.Value | base64Decode | parseJSON }}{{ range $key, $value := $p.env }}{{ $key }}={{ $value | toJSON }}\n{{ end }}", functionInvocationPrivateItem)
	for _, file := range files {
		out += fmt.Sprintf("%s={{ printf \"%%s/%s/%s\" (env \"NOMAD_SECRETS_DIR\") | toJSON }}\n", file.Env, functionInvocationFilesDir, file.Key)
	}
	return out + "NORN_REQUEST_BODY={{ $p.body | toJSON }}\nNORN_REQUEST_METHOD={{ $p.method | toJSON }}\nNORN_REQUEST_PATH={{ $p.path | toJSON }}\n{{ end }}"
}

func functionInvocationFileTemplate(path string, file FunctionInvocationFileLayout) string {
	return fmt.Sprintf("{{ with nomadVar %q }}{{ $p := .%s.Value | base64Decode | parseJSON }}{{ index $p.files %q }}{{ end }}", path, functionInvocationPrivateItem, file.Key)
}

func functionInvocationFileDestination(key string) string {
	return "secrets/" + functionInvocationFilesDir + "/" + key
}

func matchesFunctionInvocationFileTemplate(t *nomadapi.Template, path string, file FunctionInvocationFileLayout) bool {
	if t == nil || !equalStringValue(t.SourcePath, "") || !equalStringValue(t.DestPath, functionInvocationFileDestination(file.Key)) || t.EmbeddedTmpl == nil || *t.EmbeddedTmpl != functionInvocationFileTemplate(path, file) || !equalStringValue(t.ChangeMode, "restart") || !equalStringValue(t.ChangeSignal, "") || t.ChangeScript != nil || !equalBoolValue(t.Once, false) || !equalDuration(t.Splay, 5*time.Second) || !equalStringValue(t.Perms, "0400") || t.Uid != nil || t.Gid != nil || !equalStringValue(t.LeftDelim, "{{") || !equalStringValue(t.RightDelim, "}}") || !equalBoolValue(t.Envvars, false) || !equalDuration(t.VaultGrace, 0) || t.Wait != nil || !equalBoolValue(t.ErrMissingKey, true) {
		return false
	}
	return true
}

var functionInvocationFileKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,126}$`)
var functionInvocationEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validFunctionInvocationFiles(files []FunctionInvocationFileLayout) bool {
	seenKeys, seenEnv := map[string]bool{}, map[string]bool{}
	for index, file := range files {
		if !functionInvocationFileKey.MatchString(file.Key) || !functionInvocationEnvName.MatchString(file.Env) || strings.HasPrefix(file.Env, "NORN_REQUEST_") || seenKeys[file.Key] || seenEnv[file.Env] || index > 0 && files[index-1].Key >= file.Key {
			return false
		}
		seenKeys[file.Key], seenEnv[file.Env] = true, true
	}
	return true
}

func functionInvocationFiles(encoded string) ([]FunctionInvocationFileLayout, bool) {
	var files []FunctionInvocationFileLayout
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&files); err != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || !validFunctionInvocationFiles(files) {
		return nil, false
	}
	canonical, err := json.Marshal(files)
	return files, err == nil && string(canonical) == encoded
}

func equalString(a, b *string) bool                           { return a != nil && b != nil && *a == *b }
func equalStringValue(a *string, want string) bool            { return a != nil && *a == want }
func equalIntValue(a *int, want int) bool                     { return a != nil && *a == want }
func equalBoolValue(a *bool, want bool) bool                  { return a != nil && *a == want }
func equalDuration(a *time.Duration, want time.Duration) bool { return a != nil && *a == want }
func equalMap(a, b map[string]string) bool                    { return reflect.DeepEqual(a, b) }
func defaultUpdate(u *nomadapi.UpdateStrategy) bool {
	return u == nil || equalDuration(u.Stagger, 30*time.Second) && equalIntValue(u.MaxParallel, 1) && equalStringValue(u.HealthCheck, "checks") && equalDuration(u.MinHealthyTime, 10*time.Second) && equalDuration(u.HealthyDeadline, 5*time.Minute) && equalDuration(u.ProgressDeadline, 10*time.Minute) && equalIntValue(u.Canary, 0) && equalBoolValue(u.AutoRevert, false) && equalBoolValue(u.AutoPromote, false) || nomadReadBackUpdate(u)
}

// Nomad 2.0.7 returns this all-zero update object for a batch job even when
// no update strategy was registered. It carries no batch mutation policy.
func nomadReadBackUpdate(u *nomadapi.UpdateStrategy) bool {
	return u != nil && equalDuration(u.Stagger, 0) && equalIntValue(u.MaxParallel, 0) && equalStringValue(u.HealthCheck, "") && equalDuration(u.MinHealthyTime, 0) && equalDuration(u.HealthyDeadline, 0) && equalDuration(u.ProgressDeadline, 0) && equalIntValue(u.Canary, 0) && equalBoolValue(u.AutoRevert, false) && equalBoolValue(u.AutoPromote, false)
}
func defaultIdentity(i *nomadapi.WorkloadIdentity) bool {
	return i == nil || i.Name == "default" && reflect.DeepEqual(i.Audience, []string{"nomadproject.io"}) && i.ChangeMode == "" && i.ChangeSignal == "" && !i.Env && !i.File && i.Filepath == "" && i.ServiceName == "" && i.TTL == 0
}
