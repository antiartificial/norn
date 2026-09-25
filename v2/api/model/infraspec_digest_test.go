package model

import "testing"

func TestInfraSpecDigestIncludesPrivateEnvAndIsStableAcrossMapOrder(t *testing.T) {
	a := &InfraSpec{App: "example", Env: map[string]string{"A": "one", "B": "two"}, Processes: map[string]Process{"cron": {Command: "run", Schedule: "0 2 * * *", Env: map[string]string{"X": "value"}}}}
	b := &InfraSpec{App: "example", Env: map[string]string{"B": "two", "A": "one"}, Processes: map[string]Process{"cron": {Command: "run", Schedule: "0 2 * * *", Env: map[string]string{"X": "value"}}}}
	first, err := InfraSpecDigest(a)
	if err != nil {
		t.Fatal(err)
	}
	second, err := InfraSpecDigest(b)
	if err != nil || second != first {
		t.Fatalf("equivalent specs differ: %q %q %v", first, second, err)
	}
	b.Processes["cron"] = Process{Command: "different", Schedule: "0 2 * * *", Env: map[string]string{"X": "value"}}
	third, err := InfraSpecDigest(b)
	if err != nil || third == first {
		t.Fatalf("command change was not detected: %q %q %v", first, third, err)
	}
	b.Processes["cron"] = a.Processes["cron"]
	b.Env["A"] = "rotated"
	fourth, err := InfraSpecDigest(b)
	if err != nil || fourth == first {
		t.Fatalf("static env change was not detected: %q %q %v", first, fourth, err)
	}
}
