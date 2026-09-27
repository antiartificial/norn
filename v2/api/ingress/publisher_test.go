package ingress

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestPublishRenderedRouteCASAndTamperRefusal(t *testing.T) {
	directory := t.TempDir()
	input := WeightedRoute{App: "orders", Process: "web", Region: "iad", Endpoint: "https://orders.example.com", Backends: []WeightedBackend{{DeploymentID: "old", Weight: 100}}}
	first, err := RenderWeightedRoute(input)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, first.RouterName+".yaml")
	if err := PublishRenderedRoute(directory, first, ""); err != nil {
		t.Fatal(err)
	}
	if err := PublishRenderedRoute(directory, first, first.SHA256); err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	input.Backends = []WeightedBackend{{DeploymentID: "old", Weight: 70}, {DeploymentID: "new", Weight: 30}}
	second, err := RenderWeightedRoute(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := PublishRenderedRoute(directory, second, ""); err == nil {
		t.Fatal("stale absent revision overwrote route")
	}
	if err := PublishRenderedRoute(directory, second, first.SHA256); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != string(second.YAML) {
		t.Fatalf("published route mismatch: %v", err)
	}
	if err := PublishRenderedRoute(directory, first, first.SHA256); err == nil {
		t.Fatal("stale first revision overwrote second")
	}
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PublishRenderedRoute(directory, first, second.SHA256); err == nil {
		t.Fatal("tampered route was replaced without reconciliation")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(directory, "victim"), path); err != nil {
		t.Fatal(err)
	}
	if err := PublishRenderedRoute(directory, first, ""); err == nil {
		t.Fatal("symlink route was accepted")
	}
}

func TestPublishRenderedRouteConcurrentWritersOnlyOneWins(t *testing.T) {
	directory := t.TempDir()
	base := WeightedRoute{App: "orders", Process: "web", Region: "iad", Endpoint: "https://orders.example.com"}
	base.Backends = []WeightedBackend{{DeploymentID: "old", Weight: 100}}
	first, err := RenderWeightedRoute(base)
	if err != nil {
		t.Fatal(err)
	}
	base.Backends = []WeightedBackend{{DeploymentID: "new", Weight: 100}}
	second, err := RenderWeightedRoute(base)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var writers sync.WaitGroup
	for _, route := range []RenderedRoute{first, second} {
		writers.Add(1)
		go func(route RenderedRoute) {
			defer writers.Done()
			results <- PublishRenderedRoute(directory, route, "")
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
		t.Fatalf("expected one winning writer, got %d", successes)
	}
	body, err := os.ReadFile(filepath.Join(directory, first.RouterName+".yaml"))
	if err != nil || string(body) != string(first.YAML) && string(body) != string(second.YAML) {
		t.Fatalf("published route is not a complete winner: %v", err)
	}
}

func TestPublishRenderedRouteRejectsAlteredDesiredContent(t *testing.T) {
	directory := t.TempDir()
	desired, err := RenderWeightedRoute(WeightedRoute{App: "orders", Process: "web", Region: "iad", Endpoint: "https://orders.example.com", Backends: []WeightedBackend{{DeploymentID: "old", Weight: 100}}})
	if err != nil {
		t.Fatal(err)
	}
	desired.YAML = append(desired.YAML, []byte("# changed")...)
	if err := PublishRenderedRoute(directory, desired, ""); err == nil {
		t.Fatal("incorrect desired digest accepted")
	}
	sum := sha256.Sum256(desired.YAML)
	desired.SHA256 = hex.EncodeToString(sum[:])
	desired.RouterName = strings.Replace(desired.RouterName, "norn-route", "other-route", 1)
	if err := PublishRenderedRoute(directory, desired, ""); err == nil {
		t.Fatal("unscoped route name accepted")
	}
}
