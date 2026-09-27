package ingress

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func fixtureRoute(t *testing.T, deployment string) RenderedRoute {
	t.Helper()
	route, err := RenderWeightedRoute(WeightedRoute{App: "orders", Process: "web", Region: "iad", Endpoint: "https://orders.example.com", Backends: []WeightedBackend{{DeploymentID: deployment, Weight: 100}}})
	if err != nil {
		t.Fatal(err)
	}
	return route
}

func readFixtureRevision(t *testing.T, directory, routerName string) PublishedRouteRevision {
	t.Helper()
	revision, err := ReadPublishedRouteRevision(directory, routerName)
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func TestPublishedRouteGenerationFencesABAAndWithdrawal(t *testing.T) {
	directory := t.TempDir()
	first := fixtureRoute(t, "old")
	second := fixtureRoute(t, "new")
	initial := readFixtureRevision(t, directory, first.RouterName)
	if initial != (PublishedRouteRevision{}) {
		t.Fatalf("initial revision=%+v", initial)
	}
	if err := PublishRenderedRoute(directory, first, initial, 1); err != nil {
		t.Fatal(err)
	}
	r1 := readFixtureRevision(t, directory, first.RouterName)
	if r1.Generation != 1 || r1.RouteSHA256 != first.SHA256 || !r1.Present {
		t.Fatalf("first revision=%+v", r1)
	}
	if err := PublishRenderedRoute(directory, first, initial, 1); err != nil {
		t.Fatalf("exact retry failed: %v", err)
	}
	if err := PublishRenderedRoute(directory, second, r1, 2); err != nil {
		t.Fatal(err)
	}
	r2 := readFixtureRevision(t, directory, first.RouterName)
	if err := PublishRenderedRoute(directory, first, r2, 3); err != nil {
		t.Fatal(err)
	}
	r3 := readFixtureRevision(t, directory, first.RouterName)
	if r3.Generation != 3 || r3.RouteSHA256 != first.SHA256 {
		t.Fatalf("rollback revision=%+v", r3)
	}
	if err := PublishRenderedRoute(directory, second, r1, 2); err == nil {
		t.Fatal("stale A→B writer crossed A→B→A rollback")
	}
	if err := WithdrawPublishedRoute(directory, first.RouterName, r3, 4); err != nil {
		t.Fatal(err)
	}
	r4 := readFixtureRevision(t, directory, first.RouterName)
	if r4.Generation != 4 || r4.Present || r4.RouteSHA256 != "" {
		t.Fatalf("withdrawal revision=%+v", r4)
	}
	if err := WithdrawPublishedRoute(directory, first.RouterName, r3, 4); err != nil {
		t.Fatalf("withdrawal retry failed: %v", err)
	}
	if err := PublishRenderedRoute(directory, first, initial, 1); err == nil {
		t.Fatal("stale first writer reintroduced withdrawn route")
	}
	if err := PublishRenderedRoute(directory, second, r4, 5); err != nil {
		t.Fatalf("new generation after withdrawal: %v", err)
	}
}

func TestPublishedRouteRejectsTamperAndSymlink(t *testing.T) {
	directory := t.TempDir()
	desired := fixtureRoute(t, "old")
	path := filepath.Join(directory, desired.RouterName+".yaml")
	if err := PublishRenderedRoute(directory, desired, PublishedRouteRevision{}, 1); err != nil {
		t.Fatal(err)
	}
	revision := readFixtureRevision(t, directory, desired.RouterName)
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PublishRenderedRoute(directory, desired, revision, 2); err == nil {
		t.Fatal("unversioned tamper was replaced")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(directory, "victim"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPublishedRouteRevision(directory, desired.RouterName); err == nil {
		t.Fatal("symlink readback succeeded")
	}
	if err := WithdrawPublishedRoute(directory, desired.RouterName, revision, 2); err == nil {
		t.Fatal("symlink withdrawal succeeded")
	}
}

func TestPublishedRouteConcurrentGenerationOnlyOneWins(t *testing.T) {
	directory := t.TempDir()
	first := fixtureRoute(t, "old")
	second := fixtureRoute(t, "new")
	results := make(chan error, 2)
	var writers sync.WaitGroup
	for _, route := range []RenderedRoute{first, second} {
		writers.Add(1)
		go func(route RenderedRoute) {
			defer writers.Done()
			results <- PublishRenderedRoute(directory, route, PublishedRouteRevision{}, 1)
		}(route)
	}
	writers.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("expected one winner, got %d", successes)
	}
	revision := readFixtureRevision(t, directory, first.RouterName)
	if revision.Generation != 1 || revision.RouteSHA256 != first.SHA256 && revision.RouteSHA256 != second.SHA256 {
		t.Fatalf("winner revision=%+v", revision)
	}
}

func TestPublishedRouteRejectsAlteredDesiredAndUnscopedName(t *testing.T) {
	directory := t.TempDir()
	desired := fixtureRoute(t, "old")
	desired.YAML = append(desired.YAML, []byte("# changed")...)
	if err := PublishRenderedRoute(directory, desired, PublishedRouteRevision{}, 1); err == nil {
		t.Fatal("incorrect desired digest accepted")
	}
	desired = fixtureRoute(t, "old")
	desired.RouterName = strings.Replace(desired.RouterName, "norn-route", "other-route", 1)
	if err := PublishRenderedRoute(directory, desired, PublishedRouteRevision{}, 1); err == nil {
		t.Fatal("unscoped route name accepted")
	}
}

func TestPublishedRouteRejectsUnsafeWritableDirectory(t *testing.T) {
	directory := t.TempDir()
	desired := fixtureRoute(t, "directory-permissions")
	for _, mode := range []os.FileMode{0o770, 0o777} {
		if err := os.Chmod(directory, mode); err != nil {
			t.Fatal(err)
		}
		if err := PublishRenderedRoute(directory, desired, PublishedRouteRevision{}, 1); err == nil {
			t.Fatalf("unsafe route directory mode %o was accepted", mode)
		}
	}
	if os.Geteuid() != 0 {
		if err := os.Chmod(directory, 0o770|os.ModeSticky); err != nil {
			t.Fatal(err)
		}
		if err := PublishRenderedRoute(directory, desired, PublishedRouteRevision{}, 1); err == nil {
			t.Fatal("non-root sticky route directory was accepted")
		}
	}
}
