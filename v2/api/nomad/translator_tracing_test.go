package nomad

import (
	"testing"

	"norn/v2/api/model"
)

func setTestOTLPEndpoint(t *testing.T, endpoint string) {
	t.Helper()
	SetAppOTLPEndpoint(endpoint)
	t.Cleanup(func() { SetAppOTLPEndpoint("") })
}

func tracingTestSpec() *model.InfraSpec {
	return &model.InfraSpec{
		App: "web",
		Env: map[string]string{"OTEL_SERVICE_NAME": "custom"},
		Processes: map[string]model.Process{
			"web":     {Port: 8080, Env: map[string]string{"OTEL_EXPORTER_OTLP_PROTOCOL": "grpc"}},
			"nightly": {Schedule: "0 * * * *"},
			"resize":  {},
		},
	}
}

func TestTranslateInjectsOTLPEnvForServiceBatchAndPeriodic(t *testing.T) {
	setTestOTLPEndpoint(t, "http://tempo.service.consul:4318/")
	spec := tracingTestSpec()
	secrets := map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://from-secret"}

	service := map[string]string{}
	for _, tg := range mustTranslate(t, spec, "img", secrets).TaskGroups {
		if *tg.Name == "web" {
			service = tg.Tasks[0].Env
		}
	}
	periodic := mustTranslatePeriodic(t, spec, "web-nightly", spec.Processes["nightly"], "img", nil).TaskGroups[0].Tasks[0].Env
	batch := mustTranslateBatch(t, spec, "resize", spec.Processes["resize"], "img", nil, "web-resize-1").TaskGroups[0].Tasks[0].Env

	wantAttrs := "norn.app=web,norn.process=web,nomad.alloc_id=${NOMAD_ALLOC_ID},nomad.task_group=${NOMAD_GROUP_NAME}"
	if service["OTEL_RESOURCE_ATTRIBUTES"] != wantAttrs {
		t.Fatalf("service resource attributes = %q", service["OTEL_RESOURCE_ATTRIBUTES"])
	}
	// User env, process env, and secrets always win.
	if service["OTEL_SERVICE_NAME"] != "custom" || service["OTEL_EXPORTER_OTLP_PROTOCOL"] != "grpc" || service["OTEL_EXPORTER_OTLP_ENDPOINT"] != "http://from-secret" {
		t.Fatalf("injected env overrode user env: %v", service)
	}
	for name, env := range map[string]map[string]string{"periodic": periodic, "batch": batch} {
		if env["OTEL_EXPORTER_OTLP_ENDPOINT"] != "http://tempo.service.consul:4318" || env["OTEL_EXPORTER_OTLP_PROTOCOL"] != "http/protobuf" {
			t.Fatalf("%s env = %v", name, env)
		}
	}
	if periodic["OTEL_SERVICE_NAME"] != "custom" || periodic["OTEL_RESOURCE_ATTRIBUTES"] != "norn.app=web,norn.process=web-nightly,nomad.alloc_id=${NOMAD_ALLOC_ID},nomad.task_group=${NOMAD_GROUP_NAME}" {
		t.Fatalf("periodic env = %v", periodic)
	}
}

func TestTranslateOmitsOTLPEnvWhenEndpointUnset(t *testing.T) {
	setTestOTLPEndpoint(t, "")
	spec := tracingTestSpec()
	for _, tg := range mustTranslate(t, spec, "img", nil).TaskGroups {
		if _, ok := tg.Tasks[0].Env["OTEL_RESOURCE_ATTRIBUTES"]; ok {
			t.Fatalf("unexpected injected env in service group %s", *tg.Name)
		}
	}
	for _, env := range []map[string]string{
		mustTranslatePeriodic(t, spec, "web-nightly", spec.Processes["nightly"], "img", nil).TaskGroups[0].Tasks[0].Env,
		mustTranslateBatch(t, spec, "resize", spec.Processes["resize"], "img", nil, "web-resize-1").TaskGroups[0].Tasks[0].Env,
	} {
		for key := range env {
			if key == "OTEL_EXPORTER_OTLP_ENDPOINT" || key == "OTEL_RESOURCE_ATTRIBUTES" {
				t.Fatalf("unexpected injected key %s in %v", key, env)
			}
		}
	}
}
