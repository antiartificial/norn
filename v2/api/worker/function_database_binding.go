package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

// FunctionInvocationDatabaseBindingSchema identifies the public, canonical
// database binding embedded in an accepted app.function-invoke operation.
// It carries target identity and the immutable delivery revision only. It
// never carries a URL, component, TLS material, credential reference, or any
// other private database value.
const FunctionInvocationDatabaseBindingSchema = "norn.function-invocation.database-binding/v1"

// FunctionInvocationDatabaseTarget binds one logical runtime database to its
// public fencing identity.
type FunctionInvocationDatabaseTarget struct {
	Name   string                  `json:"name"`
	Target database.TargetIdentity `json:"target"`
}

// FunctionInvocationDatabaseBinding is the decoded public form of
// FunctionInvocationEffectInput.DatabaseTarget and DatabaseRevision.
// Revision is duplicated outside the JSON envelope so existing effect
// identities remain compact and can reject a mismatched pair before work.
type FunctionInvocationDatabaseBinding struct {
	Schema  string                             `json:"schema"`
	Targets []FunctionInvocationDatabaseTarget `json:"targets"`
}

// NewFunctionInvocationDatabaseBinding derives the public binding from the
// pinned spec and the already revalidated, running delivery revision. The
// caller stores Public() in FunctionInvocationEffectInput before acceptance.
func NewFunctionInvocationDatabaseBinding(spec *model.InfraSpec, delivery nomad.DatabaseRevision) (FunctionInvocationDatabaseBinding, error) {
	if spec == nil {
		return FunctionInvocationDatabaseBinding{}, fmt.Errorf("function database binding has no pinned spec")
	}
	runtime := functionRuntimeDatabaseNames(spec)
	if len(runtime) == 0 {
		if !emptyFunctionDatabaseDelivery(delivery) {
			return FunctionInvocationDatabaseBinding{}, fmt.Errorf("database-free function has delivery material")
		}
		return FunctionInvocationDatabaseBinding{}, nil
	}
	if delivery.Revision < 1 || delivery.Promoted != delivery.Revision {
		return FunctionInvocationDatabaseBinding{}, fmt.Errorf("function database delivery revision is not promoted")
	}
	if !completeFunctionRuntimeDelivery(spec, delivery) {
		return FunctionInvocationDatabaseBinding{}, fmt.Errorf("function database delivery material is incomplete")
	}
	targets, err := functionDatabaseTargets(runtime, delivery.Targets)
	if err != nil {
		return FunctionInvocationDatabaseBinding{}, err
	}
	return FunctionInvocationDatabaseBinding{Schema: FunctionInvocationDatabaseBindingSchema, Targets: targets}, nil
}

// Public returns the only two values that may enter the accepted public
// operation payload. The no-database pair is explicit so a retry cannot turn
// a database-free function into one that reads ambient delivery material.
func (b FunctionInvocationDatabaseBinding) Public(delivery nomad.DatabaseRevision) (target, revision string, err error) {
	if b.Schema == "" && len(b.Targets) == 0 {
		if !emptyFunctionDatabaseDelivery(delivery) {
			return "", "", fmt.Errorf("database-free function has delivery material")
		}
		return FunctionInvocationNoDatabase, FunctionInvocationNoDatabase, nil
	}
	if delivery.Revision < 1 || delivery.Promoted != delivery.Revision {
		return "", "", fmt.Errorf("function database delivery revision is not promoted")
	}
	encoded, err := json.Marshal(b)
	if err != nil {
		return "", "", fmt.Errorf("encode function database binding: %w", err)
	}
	parsed, err := parseFunctionInvocationDatabaseBinding(string(encoded), strconv.FormatInt(delivery.Revision, 10))
	if err != nil || !sameFunctionDatabaseTargets(parsed.Targets, b.Targets) {
		return "", "", fmt.Errorf("function database binding is invalid")
	}
	return string(encoded), strconv.FormatInt(delivery.Revision, 10), nil
}

// RecheckFunctionInvocationDatabaseBinding proves that accepted public
// target/revision fields still describe the pinned spec and exactly the
// delivery revision the worker is about to copy. It is pure and deliberately
// returns no private delivery values.
func RecheckFunctionInvocationDatabaseBinding(input FunctionInvocationEffectInput, spec *model.InfraSpec, delivery nomad.DatabaseRevision) error {
	if spec == nil || input.App != spec.App {
		return fmt.Errorf("function database binding does not match pinned app")
	}
	digest, err := model.InfraSpecDigest(spec)
	if err != nil || input.SpecDigest != digest {
		return fmt.Errorf("function database binding does not match pinned spec")
	}
	process, ok := spec.Processes[input.Process]
	if !ok || process.Function == nil {
		return fmt.Errorf("function database binding does not name a function process")
	}
	runtime := functionRuntimeDatabaseNames(spec)
	if len(runtime) == 0 {
		if input.DatabaseTarget != FunctionInvocationNoDatabase || input.DatabaseRevision != FunctionInvocationNoDatabase || !emptyFunctionDatabaseDelivery(delivery) {
			return fmt.Errorf("database-free function binding is invalid")
		}
		return nil
	}
	binding, err := parseFunctionInvocationDatabaseBinding(input.DatabaseTarget, input.DatabaseRevision)
	if err != nil {
		return err
	}
	if delivery.Revision < 1 || delivery.Promoted != delivery.Revision || input.DatabaseRevision != strconv.FormatInt(delivery.Revision, 10) {
		return fmt.Errorf("function database delivery revision does not match accepted binding")
	}
	if !completeFunctionRuntimeDelivery(spec, delivery) {
		return fmt.Errorf("function database delivery material is incomplete")
	}
	expected, err := functionDatabaseTargets(runtime, delivery.Targets)
	if err != nil {
		return err
	}
	if !sameFunctionDatabaseTargets(binding.Targets, expected) {
		return fmt.Errorf("function database target does not match running delivery")
	}
	return nil
}

func parseFunctionInvocationDatabaseBinding(target, revision string) (FunctionInvocationDatabaseBinding, error) {
	if target == FunctionInvocationNoDatabase || revision == FunctionInvocationNoDatabase {
		return FunctionInvocationDatabaseBinding{}, fmt.Errorf("database-free sentinel is invalid for a database-bound function")
	}
	parsedRevision, err := strconv.ParseInt(revision, 10, 64)
	if err != nil || parsedRevision < 1 || strconv.FormatInt(parsedRevision, 10) != revision {
		return FunctionInvocationDatabaseBinding{}, fmt.Errorf("function database revision is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(target))
	decoder.DisallowUnknownFields()
	var binding FunctionInvocationDatabaseBinding
	if err := decoder.Decode(&binding); err != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		return FunctionInvocationDatabaseBinding{}, fmt.Errorf("function database target is invalid")
	}
	canonical, err := json.Marshal(binding)
	if err != nil || !bytes.Equal(canonical, []byte(target)) || binding.Schema != FunctionInvocationDatabaseBindingSchema || len(binding.Targets) == 0 || !sortedFunctionDatabaseTargets(binding.Targets) {
		return FunctionInvocationDatabaseBinding{}, fmt.Errorf("function database target is not canonical")
	}
	for _, item := range binding.Targets {
		if item.Name == "" || !validFunctionDatabaseTarget(item.Target) {
			return FunctionInvocationDatabaseBinding{}, fmt.Errorf("function database target is invalid")
		}
	}
	return binding, nil
}

func functionRuntimeDatabaseNames(spec *model.InfraSpec) []string {
	names := make([]string, 0, len(spec.Databases))
	for _, requirement := range spec.Databases {
		if requirement.Runtime != nil {
			names = append(names, requirement.Name)
		}
	}
	sort.Strings(names)
	return names
}

func functionDatabaseTargets(names []string, delivered map[string]string) ([]FunctionInvocationDatabaseTarget, error) {
	if len(delivered) != len(names) {
		return nil, fmt.Errorf("function database delivery targets are incomplete")
	}
	targets := make([]FunctionInvocationDatabaseTarget, 0, len(names))
	for _, name := range names {
		encoded, ok := delivered[strings.ReplaceAll(name, "-", "_")]
		if !ok {
			return nil, fmt.Errorf("function database delivery target is missing")
		}
		decoder := json.NewDecoder(strings.NewReader(encoded))
		decoder.DisallowUnknownFields()
		var target database.TargetIdentity
		if err := decoder.Decode(&target); err != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || !validFunctionDatabaseTarget(target) {
			return nil, fmt.Errorf("function database delivery target is invalid")
		}
		targets = append(targets, FunctionInvocationDatabaseTarget{Name: name, Target: target})
	}
	return targets, nil
}

// completeFunctionRuntimeDelivery checks the shape of private delivery
// without copying or serializing a private value. The exact revision used by
// a function must still be runnable after it is copied to the one-shot job.
func completeFunctionRuntimeDelivery(spec *model.InfraSpec, delivery nomad.DatabaseRevision) bool {
	for _, requirement := range spec.Databases {
		if requirement.Runtime == nil {
			continue
		}
		name := strings.ReplaceAll(requirement.Name, "-", "_")
		if (requirement.Runtime.Env != "" || requirement.Runtime.FileEnv != "") && delivery.URLs[name] == "" {
			return false
		}
		if requirement.Runtime.Components != nil {
			for _, field := range []string{"host", "user", "password", "name"} {
				if delivery.Components[nomad.DatabaseComponentItemKey(requirement.Name, field)] == "" {
					return false
				}
			}
		}
		if tls := requirement.Runtime.TLS; tls != nil {
			for _, item := range []struct{ declared, material string }{
				{tls.CAFileEnv, "ca"}, {tls.ClientCertFileEnv, "client_cert"}, {tls.ClientKeyFileEnv, "client_key"},
			} {
				if item.declared != "" && delivery.TLS[nomad.DatabaseTLSItemKey(requirement.Name, item.material)] == "" {
					return false
				}
			}
		}
	}
	return true
}

func emptyFunctionDatabaseDelivery(delivery nomad.DatabaseRevision) bool {
	return delivery.Revision == 0 && delivery.Promoted == 0 && len(delivery.URLs) == 0 && len(delivery.Components) == 0 && len(delivery.TLS) == 0 && len(delivery.Targets) == 0
}

func sortedFunctionDatabaseTargets(targets []FunctionInvocationDatabaseTarget) bool {
	for index := 1; index < len(targets); index++ {
		if targets[index-1].Name >= targets[index].Name {
			return false
		}
	}
	return true
}

func sameFunctionDatabaseTargets(left, right []FunctionInvocationDatabaseTarget) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func validFunctionDatabaseTarget(target database.TargetIdentity) bool {
	return target.ServiceID != "" && target.ServiceGeneration > 0 && target.BindingID != "" && target.BindingGeneration > 0 && target.Engine != "" && target.Database != "" && target.Role != ""
}
