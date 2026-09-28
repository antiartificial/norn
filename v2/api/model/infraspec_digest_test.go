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

func TestInfraSpecDigestBindsEndpointProcess(t *testing.T) {
	spec := &InfraSpec{App: "example", Processes: map[string]Process{"web": {Port: 8080}, "admin": {Port: 9090}}, Endpoints: []Endpoint{{URL: "https://example.test", Process: "web"}}}
	webDigest, err := InfraSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Endpoints[0].Process = "admin"
	adminDigest, err := InfraSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	if webDigest == adminDigest {
		t.Fatal("endpoint process change did not change source digest")
	}
}

func TestInfraSpecDigestBindsTrafficProbe(t *testing.T) {
	spec := &InfraSpec{App: "example", Processes: map[string]Process{"web": {Port: 8080}}, Endpoints: []Endpoint{{URL: "https://example.test", Process: "web", TrafficProbe: &TrafficProbeSpec{Path: "/route-proof", BodySHA256: "c2f21436748914ff673d5bb71c7e5fb50bcc64eced18dae230eface10d65b1cc"}}}}
	first, err := InfraSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Endpoints[0].TrafficProbe.Path = "/other"
	second, err := InfraSpecDigest(spec)
	if err != nil || first == second {
		t.Fatalf("changed probe did not change signed source digest: %q %q %v", first, second, err)
	}
}
