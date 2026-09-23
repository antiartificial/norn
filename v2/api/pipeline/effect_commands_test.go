package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestBuildTestSubjectIsTheRecordedSourceIdentityOnEveryClaim(t *testing.T) {
	identity := "sha256:" + strings.Repeat("a", 64)
	for generation := int64(1); generation <= 3; generation++ {
		claim, err := store.NewOperationClaim("operation-1", "worker", generation)
		if err != nil {
			t.Fatal(err)
		}
		// Neither the claim, a dirty flag nor a synthetic commit label may
		// change the subject; only the recorded source identity does.
		st := &state{spec: &model.InfraSpec{App: "demo"}, commitSHA: "local-2026092" + string(rune('0'+generation)), sourceDirty: generation%2 == 0, claim: claim, sourceIdentity: identity}
		if got := buildTestSubject(st); got != "source:"+identity {
			t.Fatalf("claim %d subject = %q", generation, got)
		}
		if buildTestResource(st) != "app/demo/build.test" {
			t.Fatalf("resource = %q", buildTestResource(st))
		}
	}
	claim, _ := store.NewOperationClaim("operation-1", "worker", 1)
	p := &Pipeline{BuildTestEffects: &BuildTestEffects{Executor: &effect.Executor{}, Store: nil, Supervisor: "x", Timeout: 1}}
	p.BuildTestEffects.Descriptor = func(effect.LaunchMaterial) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }
	p.BuildTestEffects.Store = unusedEffectStore{}
	if err := p.runSupervisedTest(context.Background(), &state{spec: &model.InfraSpec{App: "demo", Build: &model.BuildSpec{Test: "true"}}, claim: claim}); err == nil || !strings.Contains(err.Error(), "source identity") {
		t.Fatalf("supervised test without a recorded source identity = %v", err)
	}
}

type unusedEffectStore struct{}

func (unusedEffectStore) Authority(context.Context) (string, error) { return "", nil }
func (unusedEffectStore) UnresolvedForResource(context.Context, string, string) (effect.Record, bool, error) {
	return effect.Record{}, false, nil
}

func TestTreeDigestIdentifiesContentModeAndLinksIgnoringGit(t *testing.T) {
	build := func(t *testing.T) string {
		root := t.TempDir()
		for path, contents := range map[string]string{"a.txt": "alpha", "dir/b.sh": "#!/bin/sh\n", ".git/HEAD": "ref: main\n"} {
			full := filepath.Join(root, path)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink("a.txt", filepath.Join(root, "link")); err != nil {
			t.Fatal(err)
		}
		return root
	}
	digest := func(t *testing.T, root string) string {
		value, err := treeDigest(root)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	baseline := digest(t, build(t))
	if other := digest(t, build(t)); other != baseline {
		t.Fatal("identical trees produced different digests")
	}
	for name, mutate := range map[string]func(t *testing.T, root string){
		"git metadata only": func(t *testing.T, root string) {
			_ = os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("changed"), 0o644)
		},
	} {
		root := build(t)
		mutate(t, root)
		if digest(t, root) != baseline {
			t.Fatalf("%s changed the digest", name)
		}
	}
	for name, mutate := range map[string]func(t *testing.T, root string){
		"content":        func(t *testing.T, root string) { _ = os.WriteFile(filepath.Join(root, "a.txt"), []byte("beta"), 0o644) },
		"executable bit": func(t *testing.T, root string) { _ = os.Chmod(filepath.Join(root, "dir", "b.sh"), 0o755) },
		"new file":       func(t *testing.T, root string) { _ = os.WriteFile(filepath.Join(root, "c.txt"), nil, 0o644) },
		"empty dir":      func(t *testing.T, root string) { _ = os.Mkdir(filepath.Join(root, "empty"), 0o755) },
		"link target": func(t *testing.T, root string) {
			_ = os.Remove(filepath.Join(root, "link"))
			_ = os.Symlink("dir/b.sh", filepath.Join(root, "link"))
		},
		"renamed": func(t *testing.T, root string) {
			_ = os.Rename(filepath.Join(root, "a.txt"), filepath.Join(root, "z.txt"))
		},
	} {
		root := build(t)
		mutate(t, root)
		if digest(t, root) == baseline {
			t.Fatalf("%s did not change the digest", name)
		}
	}
	root := build(t)
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o600); err != nil {
		t.Skip(err)
	}
	if _, err := treeDigest(root); err == nil {
		t.Fatal("special file was silently ignored")
	}
}

func TestSameSourceRequiresIdenticalRecordedIdentity(t *testing.T) {
	sha := strings.Repeat("a", 40)
	recorded := sourceCheckpoint{SourceKind: "git_clone", CommitSHA: sha, SourceRef: "main", TreeDigest: "sha256:1", SourceChanges: []string{}}
	if err := sameSource(recorded, recorded); err != nil {
		t.Fatal(err)
	}
	branchMoved := recorded
	branchMoved.SourceRef = sha
	if err := sameSource(recorded, branchMoved); err != nil {
		t.Fatalf("pinned re-checkout with a different ref label rejected: %v", err)
	}
	synthetic := sourceCheckpoint{SourceKind: "local_copy", CommitSHA: "local-1", TreeDigest: "sha256:2", SourceChanges: []string{}}
	laterLabel := synthetic
	laterLabel.CommitSHA = "local-2"
	if err := sameSource(synthetic, laterLabel); err != nil {
		t.Fatalf("synthetic local commit label rejected unchanged content: %v", err)
	}
	for name, mutate := range map[string]func(*sourceCheckpoint){
		"content": func(c *sourceCheckpoint) { c.TreeDigest = "sha256:9" },
		"kind":    func(c *sourceCheckpoint) { c.SourceKind = "local_fallback" },
		"commit":  func(c *sourceCheckpoint) { c.CommitSHA = strings.Repeat("b", 40) },
		"dirty":   func(c *sourceCheckpoint) { c.SourceDirty = true },
		"changes": func(c *sourceCheckpoint) { c.SourceChanges = []string{"file"} },
	} {
		changed := recorded
		mutate(&changed)
		var identityErr *SourceIdentityChangedError
		if err := sameSource(recorded, changed); !errors.As(err, &identityErr) {
			t.Fatalf("%s change accepted: %v", name, err)
		}
	}
}

func TestBuildTestExecutionIDSeparatesOperationsInputsAndClaims(t *testing.T) {
	base := effect.Reservation{Authority: "authority", InputDigest: "sha256:" + strings.Repeat("0", 64), OperationClaim: effect.OperationClaim{OperationID: "operation-1", Generation: 1}}
	seen := map[string]bool{buildTestExecutionID(base): true}
	for _, mutate := range []func(*effect.Reservation){
		func(r *effect.Reservation) { r.OperationClaim.OperationID = "operation-2" },
		func(r *effect.Reservation) { r.InputDigest = "sha256:" + strings.Repeat("1", 64) },
		func(r *effect.Reservation) { r.OperationClaim.Generation = 2 },
		func(r *effect.Reservation) { r.Authority = "other" },
	} {
		changed := base
		mutate(&changed)
		id := buildTestExecutionID(changed)
		if seen[id] {
			t.Fatalf("execution ID collision for %+v", changed)
		}
		seen[id] = true
	}
	again := base
	if buildTestExecutionID(again) != buildTestExecutionID(base) || !strings.HasPrefix(buildTestExecutionID(base), "build-test-") {
		t.Fatal("execution ID is not deterministic")
	}
}

func TestTestFailureMessageKeepsBoundedTail(t *testing.T) {
	output := []byte(strings.Repeat("x", maxTestFailureBytes) + "FINAL LINE")
	message := tail(output, maxTestFailureBytes)
	if !strings.HasSuffix(message, "FINAL LINE") || len(message) > maxTestFailureBytes+len("…") {
		t.Fatalf("tail length=%d suffix ok=%v", len(message), strings.HasSuffix(message, "FINAL LINE"))
	}
}
