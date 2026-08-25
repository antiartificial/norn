package pipeline

import (
	"os"
	"strings"
	"testing"
)

func TestGitEnvKeepsTokenOutOfAskpassScriptAndEnvironment(t *testing.T) {
	const token = "secret-with-'quotes"
	p := &Pipeline{GitToken: token}
	env, cleanup := p.gitEnv("https://example.test/private/repository.git")
	if cleanup == nil {
		t.Fatal("expected temporary credential cleanup")
	}
	defer cleanup()

	values := map[string]string{}
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			values[key] = value
		}
		if strings.Contains(item, token) {
			t.Fatal("token leaked into child environment")
		}
	}
	scriptPath := values["GIT_ASKPASS"]
	tokenPath := values["NORN_GIT_ASKPASS_TOKEN_FILE"]
	if scriptPath == "" || tokenPath == "" {
		t.Fatalf("missing askpass paths: %#v", values)
	}
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(script), token) {
		t.Fatal("token leaked into askpass script")
	}
	stored, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != token {
		t.Fatal("token file content changed")
	}
	cleanup()
	if _, err := os.Stat(scriptPath); !os.IsNotExist(err) {
		t.Fatalf("askpass script was not removed: %v", err)
	}
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Fatalf("token file was not removed: %v", err)
	}
}

func TestGitEnvRejectsMultilineToken(t *testing.T) {
	p := &Pipeline{GitToken: "token\ninjected"}
	env, cleanup := p.gitEnv("https://example.test/private/repository.git")
	if len(env) != 0 || cleanup != nil {
		t.Fatalf("multiline token must not create askpass credentials: env=%#v", env)
	}
}
