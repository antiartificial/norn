package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"norn/v2/api/cloudflared"
	"norn/v2/api/effect"
)

func TestLocalCloudflaredReceiptIsCreateOnlyAndPrivate(t *testing.T) {
	previous := cloudflared.ConfigPath()
	cloudflared.SetConfigPath(filepath.Join(t.TempDir(), "config.yml"))
	t.Cleanup(func() { cloudflared.SetConfigPath(previous) })
	driver := localCloudflaredDriver{}
	id := "cloudflared-test-receipt"
	first := []byte(`{"executionId":"cloudflared-test-receipt"}`)
	if err := driver.WriteReceipt(id, first); err != nil {
		t.Fatal(err)
	}
	if err := driver.WriteReceipt(id, first); err != nil {
		t.Fatalf("exact receipt retry: %v", err)
	}
	if err := driver.WriteReceipt(id, []byte(`{"executionId":"different"}`)); err == nil {
		t.Fatal("different receipt replaced the original")
	}
	actual, err := driver.ReadReceipt(id)
	if err != nil || string(actual) != string(first) {
		t.Fatalf("receipt=%q err=%v", actual, err)
	}
	link := cloudflaredReceiptPath("cloudflared-symlink")
	if err := os.Symlink(cloudflaredReceiptPath(id), link); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.ReadReceipt("cloudflared-symlink"); err == nil {
		t.Fatal("symlink receipt was trusted")
	}
	hardlink := cloudflaredReceiptPath("cloudflared-hardlink")
	if err := os.Link(cloudflaredReceiptPath(id), hardlink); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.ReadReceipt(id); err == nil {
		t.Fatal("multiply linked receipt was trusted")
	}
}

type fakeCloudflaredDriver struct {
	host                     string
	config                   cloudflared.Config
	receipts                 map[string][]byte
	applyCalls, restartCalls int
	afterApply, afterRestart func() error
}

func (f *fakeCloudflaredDriver) Read(context.Context) (*cloudflared.Config, string, error) {
	copy := f.config
	copy.Ingress = append([]cloudflared.IngressRule(nil), f.config.Ingress...)
	digest, err := cloudflared.ConfigDigest(&copy)
	return &copy, digest, err
}
func (f *fakeCloudflaredDriver) Apply(_ context.Context, c *cloudflared.Config) error {
	f.applyCalls++
	f.config = *c
	if f.afterApply != nil {
		return f.afterApply()
	}
	return nil
}
func (f *fakeCloudflaredDriver) Restart(context.Context) error {
	f.restartCalls++
	if f.afterRestart != nil {
		return f.afterRestart()
	}
	return nil
}
func (f *fakeCloudflaredDriver) ReadReceipt(id string) ([]byte, error) {
	b, ok := f.receipts[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	return b, nil
}
func (f *fakeCloudflaredDriver) WriteReceipt(id string, b []byte) error {
	if f.receipts == nil {
		f.receipts = map[string][]byte{}
	}
	f.receipts[id] = b
	return nil
}
func (f *fakeCloudflaredDriver) Host() string {
	if f.host != "" {
		return f.host
	}
	return "mini-a"
}
func (*fakeCloudflaredDriver) Path() string { return "/private/config.yml" }

func cloudflaredTestReservation(t *testing.T, f *fakeCloudflaredDriver) effect.Reservation {
	t.Helper()
	before, err := cloudflared.ConfigDigest(&f.config)
	if err != nil {
		t.Fatal(err)
	}
	m := cloudflared.Mutation{Action: "enable", App: "demo", Host: f.Host(), ConfigPath: f.Path(), Hostnames: []string{"https://demo.example.com"}, Service: "http://127.0.0.1:8080", BeforeDigest: before}
	cfg, _, err := f.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	m.AfterDigest, err = cloudflared.ConfigDigest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return effect.Reservation{Authority: "test", Resource: "host/mini-a/cloudflared", Stage: cloudflaredMutationKind, Supervisor: "cloudflared-host", SupervisorExecutionID: "cloudflared-123", LaunchPayload: b}
}

func TestCloudflaredCrashAfterConfigWriteHoldsWithoutRestartReplay(t *testing.T) {
	ctx := context.Background()
	f := &fakeCloudflaredDriver{config: cloudflared.Config{Ingress: []cloudflared.IngressRule{{Service: "http_status:404"}}}}
	r := cloudflaredTestReservation(t, f)
	s := &cloudflaredSupervisor{driver: f}
	f.afterApply = func() error { return errors.New("simulated process death after config write") }
	if err := s.Prepare(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Launch(ctx, r, effect.LaunchMaterial{}); err == nil {
		t.Fatal("launch unexpectedly completed")
	}
	if f.applyCalls != 1 || f.restartCalls != 0 {
		t.Fatalf("writes=%d restarts=%d", f.applyCalls, f.restartCalls)
	}
	if err := s.Prepare(ctx, r); err == nil {
		t.Fatal("ambiguous post-write state became repeat safe")
	}
	obs, err := s.Query(ctx, r, effect.ExecutionIdentity{})
	if err != nil || obs.Phase != effect.SupervisorUnknown {
		t.Fatalf("observation=%+v err=%v", obs, err)
	}
	if f.restartCalls != 0 {
		t.Fatal("recovery restarted cloudflared")
	}
}

func TestCloudflaredCrashAfterRestartHoldsUntilLocalReceipt(t *testing.T) {
	ctx := context.Background()
	f := &fakeCloudflaredDriver{config: cloudflared.Config{Ingress: []cloudflared.IngressRule{{Service: "http_status:404"}}}}
	r := cloudflaredTestReservation(t, f)
	s := &cloudflaredSupervisor{driver: f}
	f.afterRestart = func() error { return errors.New("simulated lost restart response") }
	if _, err := s.Launch(ctx, r, effect.LaunchMaterial{}); err == nil {
		t.Fatal("restart ambiguity unexpectedly completed")
	}
	if err := s.Prepare(ctx, r); err == nil {
		t.Fatal("ambiguous restart became repeat safe")
	}
	obs, err := s.Query(ctx, r, effect.ExecutionIdentity{})
	if err != nil || obs.Phase != effect.SupervisorUnknown {
		t.Fatalf("observation=%+v err=%v", obs, err)
	}
	if f.restartCalls != 1 {
		t.Fatalf("restart calls=%d", f.restartCalls)
	}
}

func TestCloudflaredReceiptRecoversExactRestartWithoutAnotherLaunch(t *testing.T) {
	ctx := context.Background()
	f := &fakeCloudflaredDriver{config: cloudflared.Config{Ingress: []cloudflared.IngressRule{{Service: "http_status:404"}}}}
	r := cloudflaredTestReservation(t, f)
	s := &cloudflaredSupervisor{driver: f}
	id, err := s.Launch(ctx, r, effect.LaunchMaterial{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Prepare(ctx, r); err != nil {
		t.Fatal(err)
	}
	obs, err := s.Query(ctx, r, id)
	if err != nil || obs.Phase != effect.SupervisorSucceeded {
		t.Fatalf("observation=%+v err=%v", obs, err)
	}
	verification, err := (cloudflaredVerifier{}).Verify(ctx, effect.Record{Reservation: r}, obs)
	if err != nil || verification.ResultDigest != effect.DigestInput(obs.Output) {
		t.Fatalf("verification=%+v err=%v", verification, err)
	}
	if f.applyCalls != 1 || f.restartCalls != 1 {
		t.Fatalf("writes=%d restarts=%d", f.applyCalls, f.restartCalls)
	}
}
