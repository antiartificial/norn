package nomad

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"testing"
	"time"

	"norn/v2/api/model"
)

// TestVerifyRunningAppImageInNomad uses only a disposable Docker-enabled
// Nomad agent. It owns and purges its uniquely named service job.
func TestVerifyRunningAppImageInNomad(t *testing.T) {
	address, image := os.Getenv("NORN_TEST_NOMAD_ADDR"), os.Getenv("NORN_TEST_FUNCTION_IMAGE")
	if address == "" || image == "" || os.Getenv("NORN_TEST_NOMAD_DOCKER") != "1" {
		t.Skip("set NORN_TEST_NOMAD_ADDR, NORN_TEST_FUNCTION_IMAGE, and NORN_TEST_NOMAD_DOCKER=1 for a disposable Nomad Docker agent")
	}
	endpoint, err := url.Parse(address)
	if err != nil || endpoint.Scheme != "http" || endpoint.Hostname() == "" || net.ParseIP(endpoint.Hostname()) == nil || !net.ParseIP(endpoint.Hostname()).IsLoopback() || !model.IsContentAddressedImage(image) {
		t.Fatal("running-image qualification requires a loopback disposable Nomad and content-addressed image")
	}
	client, err := NewClient(address)
	if err != nil {
		t.Fatal(err)
	}
	app := fmt.Sprintf("norn-fn-image-%d", time.Now().UnixNano())
	spec := &model.InfraSpec{App: app, Deploy: true, Processes: map[string]model.Process{"web": {Command: "sleep 120"}, "resize": {Command: "true", Function: &model.FunctionSpec{}}}}
	job := TranslateForRegion(spec, image, nil, spec.ResolvedRegions()[0])
	job.TaskGroups[0].Tasks[0].Config["force_pull"] = false
	if _, _, err := client.api.Jobs().Register(job, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _, _ = client.api.Jobs().Deregister(app, true, nil) })
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for {
		err = client.VerifyRunningAppImage(ctx, spec, image)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("running image never became provable: %v", err)
		}
		time.Sleep(time.Second)
	}
	if err := client.VerifyRunningAppImage(ctx, spec, "registry.example/other@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); !errors.Is(err, ErrRunningAppImageUnproven) {
		t.Fatalf("wrong image read-back err=%v", err)
	}
}
