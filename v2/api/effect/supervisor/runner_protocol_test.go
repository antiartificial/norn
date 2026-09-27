package supervisor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"norn/v2/api/effect"
)

const runnerSecretCanary = "NORN_RUNNER_SECRET_CANARY_5d21"

func helperRequest(t *testing.T, directory, command string, timeout time.Duration) (runnerRequest, []byte) {
	t.Helper()
	key := runnerStatusKey(testSigningKey, "runtime-"+filepath.Base(directory))
	request := runnerRequest{
		Protocol:  ProtocolV1,
		Execution: BackendExecution{SupervisorExecutionID: "execution-" + filepath.Base(directory), RuntimeInstanceID: "runtime-" + filepath.Base(directory), StateDirectory: directory},
		Material: effect.LaunchMaterial{
			Argv: []string{"sh", "-c", command}, Directory: t.TempDir(),
			Environment: []string{"PATH=/usr/bin:/bin", "TOKEN=" + runnerSecretCanary}, Subject: "git:" + strings.Repeat("b", 40), Timeout: timeout,
		},
		StatusKey: hex.EncodeToString(key),
	}
	return request, key
}

func runHelperRequest(t *testing.T, request runnerRequest) error {
	t.Helper()
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return RunHelper(bytes.NewReader(encoded))
}

func TestRunHelperBindsOutputLengthAndDigestInSignedTerminalStatus(t *testing.T) {
	for name, test := range map[string]struct {
		command string
		phase   effect.SupervisorPhase
		exit    int
		output  string
	}{
		"success": {"printf 'ok:%s' \"$TOKEN\" | cut -c1-3", effect.SupervisorSucceeded, 0, "ok:\n"},
		"failure": {"printf 'broken' >&2; exit 3", effect.SupervisorFailed, 3, "broken"},
	} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			request, key := helperRequest(t, directory, test.command, time.Minute)
			if err := runHelperRequest(t, request); err != nil {
				t.Fatal(err)
			}
			status, err := readRunnerStatus(directory, key, request.Execution.RuntimeInstanceID)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256([]byte(test.output))
			if status.Phase != test.phase || status.ExitCode == nil || *status.ExitCode != test.exit ||
				status.ResultBytes != int64(len(test.output)) || status.ResultSHA256 != hex.EncodeToString(digest[:]) || status.TimedOut {
				t.Fatalf("terminal status = %+v", status)
			}
			output, err := readVerifiedResult(directory, status)
			if err != nil || string(output) != test.output {
				t.Fatalf("verified output = %q, %v", output, err)
			}
		})
	}
}

func TestRunHelperBoundsStoredOutputAndCountsDiscardedBytes(t *testing.T) {
	directory := t.TempDir()
	extra := 4096
	request, key := helperRequest(t, directory, "head -c "+strconv.Itoa(MaxResultBytes+extra)+" /dev/zero", time.Minute)
	if err := runHelperRequest(t, request); err != nil {
		t.Fatal(err)
	}
	status, err := readRunnerStatus(directory, key, request.Execution.RuntimeInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(directory, "result.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if status.Phase != effect.SupervisorSucceeded || status.ResultBytes != MaxResultBytes || status.OutputDiscarded != int64(extra) || info.Size() != MaxResultBytes {
		t.Fatalf("bounded capture status=%+v size=%d", status, info.Size())
	}
	if output, err := readVerifiedResult(directory, status); err != nil || len(output) != MaxResultBytes {
		t.Fatalf("bounded output len=%d err=%v", len(output), err)
	}
}

func TestRunHelperTimeoutKillsProcessGroupAndRecordsFinalFailure(t *testing.T) {
	directory := t.TempDir()
	marker := filepath.Join(t.TempDir(), "survivor")
	request, key := helperRequest(t, directory, "(sleep 2; touch '"+marker+"') & sleep 30", 200*time.Millisecond)
	started := time.Now()
	if err := runHelperRequest(t, request); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 8*time.Second {
		t.Fatalf("timeout did not stop the command promptly: %s", elapsed)
	}
	status, err := readRunnerStatus(directory, key, request.Execution.RuntimeInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Phase != effect.SupervisorFailed || !status.TimedOut || status.ExitCode == nil || *status.ExitCode != -1 {
		t.Fatalf("timeout status = %+v", status)
	}
	time.Sleep(2500 * time.Millisecond)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("background descendant in the command process group survived the timeout kill")
	}
}

func TestRunHelperRejectsMalformedRequestsWithoutEchoingThem(t *testing.T) {
	directory := t.TempDir()
	request, _ := helperRequest(t, directory, "echo "+runnerSecretCanary, time.Minute)
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var withUnknown map[string]any
	_ = json.Unmarshal(encoded, &withUnknown)
	withUnknown["extra"] = runnerSecretCanary
	unknown, _ := json.Marshal(withUnknown)
	noTimeout := request
	noTimeout.Material.Timeout = 0
	missingTimeout, _ := json.Marshal(noTimeout)
	for name, input := range map[string][]byte{
		"trailing data":  append(append([]byte(nil), encoded...), []byte(` {"`+runnerSecretCanary+`":1}`)...),
		"unknown field":  unknown,
		"truncated":      encoded[:len(encoded)/2],
		"oversized":      append(bytes.Repeat([]byte(" "), maxRunnerRequestBytes), encoded...),
		"missing bounds": missingTimeout,
	} {
		t.Run(name, func(t *testing.T) {
			err := RunHelper(bytes.NewReader(input))
			if err == nil {
				t.Fatal("malformed runner request accepted")
			}
			if strings.Contains(err.Error(), runnerSecretCanary) {
				t.Fatalf("runner error echoed request content: %v", err)
			}
			if _, statErr := os.Stat(filepath.Join(directory, "runner-status.json")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatal("rejected request still published runner status")
			}
		})
	}
}

func TestVerifiedResultRejectsReplacementTruncationGrowthAndOversize(t *testing.T) {
	newResult := func(t *testing.T) (string, runnerStatus) {
		directory := t.TempDir()
		request, key := helperRequest(t, directory, "printf 'authentic output'", time.Minute)
		if err := runHelperRequest(t, request); err != nil {
			t.Fatal(err)
		}
		status, err := readRunnerStatus(directory, key, request.Execution.RuntimeInstanceID)
		if err != nil {
			t.Fatal(err)
		}
		return directory, status
	}
	for name, tamper := range map[string]func(t *testing.T, path string){
		"appended":  func(t *testing.T, path string) { appendFile(t, path, []byte("!")) },
		"truncated": func(t *testing.T, path string) { _ = os.Truncate(path, 4) },
		"rewritten": func(t *testing.T, path string) { _ = os.WriteFile(path, []byte("forged output!!!"), 0o600) },
		"symlink": func(t *testing.T, path string) {
			other := filepath.Join(t.TempDir(), "other")
			_ = os.WriteFile(other, []byte("authentic output"), 0o600)
			_ = os.Remove(path)
			if err := os.Symlink(other, path); err != nil {
				t.Fatal(err)
			}
		},
		"oversized": func(t *testing.T, path string) { _ = os.Truncate(path, MaxResultBytes+1) },
		"fifo": func(t *testing.T, path string) {
			_ = os.Remove(path)
			if err := mkfifo(path); err != nil {
				t.Skip(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			directory, status := newResult(t)
			tamper(t, filepath.Join(directory, "result.bin"))
			done := make(chan error, 1)
			go func() { _, err := readVerifiedResult(directory, status); done <- err }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("tampered result was accepted")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("result read blocked")
			}
		})
	}
	directory, status := newResult(t)
	status.ResultBytes++
	if _, err := readVerifiedResult(directory, status); err == nil {
		t.Fatal("result length disagreement accepted")
	}
	running := status
	running.Phase = effect.SupervisorRunning
	if _, err := readVerifiedResult(directory, running); err == nil {
		t.Fatal("result without terminal status accepted")
	}
}

func TestRunnerStatusRejectsTamperingAndIncompleteTerminalStatus(t *testing.T) {
	directory := t.TempDir()
	request, key := helperRequest(t, directory, "printf ok", time.Minute)
	if err := runHelperRequest(t, request); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "runner-status.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readRunnerStatus(directory, key, "other-runtime"); err == nil {
		t.Fatal("status for another runtime accepted")
	}
	if _, err := readRunnerStatus(directory, runnerStatusKey([]byte(strings.Repeat("x", 32)), request.Execution.RuntimeInstanceID), request.Execution.RuntimeInstanceID); err == nil {
		t.Fatal("status accepted under another key")
	}
	forged := bytes.Replace(original, []byte(`"resultBytes":2`), []byte(`"resultBytes":3`), 1)
	if bytes.Equal(forged, original) {
		t.Fatal("fixture did not change result length")
	}
	if err := os.WriteFile(path, forged, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRunnerStatus(directory, key, request.Execution.RuntimeInstanceID); err == nil {
		t.Fatal("forged terminal length accepted")
	}
	exit := 0
	if err := writeRunnerStatus(directory, key, runnerStatus{Protocol: ProtocolV1, RuntimeInstanceID: request.Execution.RuntimeInstanceID, Phase: effect.SupervisorSucceeded, ExitCode: &exit}); err != nil {
		t.Fatal(err)
	}
	if _, err := readRunnerStatus(directory, key, request.Execution.RuntimeInstanceID); err == nil {
		t.Fatal("signed terminal status without a result digest accepted")
	}
}

func TestBoundedCaptureReportsStorageFailureWithoutFailingTheCommand(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "capture")
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	capture := &boundedCapture{file: file, hash: sha256.New(), limit: 10}
	if written, err := capture.Write([]byte("data")); written != 4 || err != nil {
		t.Fatalf("write = %d, %v", written, err)
	}
	if capture.err == nil {
		t.Fatal("storage failure was not recorded for status suppression")
	}
}

func appendFile(t *testing.T, path string, data []byte) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
}
