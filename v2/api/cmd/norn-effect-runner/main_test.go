package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"norn/v2/api/effect/supervisor"
)

func TestMain(m *testing.M) {
	if os.Getenv("NORN_EFFECT_RUNNER_TEST_EXEC") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runner(t *testing.T, stdin string, arguments ...string) (string, error) {
	t.Helper()
	command := exec.Command(os.Args[0], arguments...)
	command.Env = []string{"NORN_EFFECT_RUNNER_TEST_EXEC=1", "PATH=/usr/bin:/bin"}
	command.Stdin = strings.NewReader(stdin)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	return output.String(), err
}

func TestRunnerEntryPointProtocolHandshakeAndRejection(t *testing.T) {
	output, err := runner(t, "", supervisor.ProtocolFlag)
	if err != nil || strings.TrimSpace(output) != supervisor.ProtocolV1 {
		t.Fatalf("protocol handshake = %q, %v", output, err)
	}
	if _, err := runner(t, "", "--unexpected"); err == nil {
		t.Fatal("unknown argument accepted")
	}
	output, err = runner(t, `{"protocol":"norn.effect-runner/v1","secret":"NORN_ENTRY_CANARY"}`)
	if err == nil || strings.Contains(output, "NORN_ENTRY_CANARY") {
		t.Fatalf("malformed request = %q, %v", output, err)
	}
}
