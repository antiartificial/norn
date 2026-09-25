package worker

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"norn/v2/api/store"
)

func TestEncodeFunctionInvocationRuntimeMaterialMergesPrivateEnv(t *testing.T) {
	request := store.PrivateInvocationInput{Body: "line one\nline two", Method: "", Path: "/x?q=é"}
	encoded, err := EncodeFunctionInvocationRuntimeMaterial(request,
		map[string]string{"APP": "app", "SHARED": "app"},
		map[string]string{"SECRET": "private\nquoted \"value\"", "SHARED": "secret"},
		map[string]string{"SHARED": "process", "EMPTY": ""})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Body, Method, Path string
		Env                map[string]string
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	empty, present := got.Env["EMPTY"]
	if got.Body != request.Body || got.Method != request.Method || got.Path != request.Path || got.Env["APP"] != "app" || got.Env["SHARED"] != "process" || got.Env["SECRET"] != "private\nquoted \"value\"" || !present || empty != "" {
		t.Fatalf("runtime material did not preserve precedence and bytes: %+v", got)
	}
}

func TestEncodeFunctionInvocationRuntimeMaterialRejectsInvalidKeysWithoutValueLeak(t *testing.T) {
	const canary = "private-secret-canary"
	for _, name := range []string{"BAD\nKEY", "NORN_REQUEST_BODY", "9BAD"} {
		_, err := EncodeFunctionInvocationRuntimeMaterial(store.PrivateInvocationInput{}, nil, map[string]string{name: canary}, nil)
		if !errors.Is(err, ErrFunctionRuntimeEnvironment) || strings.Contains(err.Error(), canary) {
			t.Fatalf("invalid environment name %q: %v", name, err)
		}
	}
}
