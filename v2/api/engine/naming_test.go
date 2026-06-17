package engine

import (
	"testing"
)

func TestContainerName(t *testing.T) {
	tests := []struct {
		app, process string
		replica      int
		want         string
	}{
		{"myapp", "web", 0, "norn-myapp-web-0"},
		{"myapp", "web", 2, "norn-myapp-web-2"},
		{"field-harbor", "worker", 0, "norn-field-harbor-worker-0"},
	}
	for _, tt := range tests {
		got := ContainerName(tt.app, tt.process, tt.replica)
		if got != tt.want {
			t.Errorf("ContainerName(%q, %q, %d) = %q, want %q", tt.app, tt.process, tt.replica, got, tt.want)
		}
	}
}

func TestCanaryName(t *testing.T) {
	got := CanaryName("myapp", "web", 0)
	want := "norn-myapp-web-canary-0"
	if got != want {
		t.Errorf("CanaryName = %q, want %q", got, want)
	}
}

func TestCronRunName(t *testing.T) {
	got := CronRunName("myapp", "backup", 1718000000)
	want := "norn-myapp-backup-cron-1718000000"
	if got != want {
		t.Errorf("CronRunName = %q, want %q", got, want)
	}
}

func TestBatchName(t *testing.T) {
	got := BatchName("myapp", "invoke", 1718000000)
	want := "norn-myapp-invoke-fn-1718000000"
	if got != want {
		t.Errorf("BatchName = %q, want %q", got, want)
	}
}

func TestIsNornContainer(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"norn-myapp-web-0", true},
		{"norn-x-y-0", true},
		{"docker-something", false},
		{"norn", false},
		{"", false},
	}
	for _, tt := range tests {
		got := IsNornContainer(tt.name)
		if got != tt.want {
			t.Errorf("IsNornContainer(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestParseContainerName_Service(t *testing.T) {
	SetKnownApps([]string{"myapp", "field-harbor"})
	defer SetKnownApps(nil)

	tests := []struct {
		name    string
		wantApp string
		wantProc string
		wantRep int
	}{
		{"norn-myapp-web-0", "myapp", "web", 0},
		{"norn-myapp-web-3", "myapp", "web", 3},
		{"norn-field-harbor-worker-1", "field-harbor", "worker", 1},
	}
	for _, tt := range tests {
		p, err := ParseContainerName(tt.name)
		if err != nil {
			t.Errorf("ParseContainerName(%q) error: %v", tt.name, err)
			continue
		}
		if p.App != tt.wantApp || p.Process != tt.wantProc || p.Replica != tt.wantRep || p.Kind != "service" {
			t.Errorf("ParseContainerName(%q) = %+v, want app=%q proc=%q rep=%d kind=service",
				tt.name, p, tt.wantApp, tt.wantProc, tt.wantRep)
		}
	}
}

func TestParseContainerName_Canary(t *testing.T) {
	SetKnownApps([]string{"myapp"})
	defer SetKnownApps(nil)

	p, err := ParseContainerName("norn-myapp-web-canary-0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Kind != "canary" || p.App != "myapp" || p.Process != "web" || p.Replica != 0 {
		t.Errorf("got %+v, want canary myapp/web/0", p)
	}
}

func TestParseContainerName_Cron(t *testing.T) {
	SetKnownApps([]string{"myapp"})
	defer SetKnownApps(nil)

	p, err := ParseContainerName("norn-myapp-backup-cron-1718000000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Kind != "cron" || p.App != "myapp" || p.Process != "backup" || p.Ts != 1718000000 {
		t.Errorf("got %+v, want cron myapp/backup/ts=1718000000", p)
	}
}

func TestParseContainerName_Batch(t *testing.T) {
	SetKnownApps([]string{"myapp"})
	defer SetKnownApps(nil)

	p, err := ParseContainerName("norn-myapp-invoke-fn-1718000000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Kind != "batch" || p.App != "myapp" || p.Process != "invoke" || p.Ts != 1718000000 {
		t.Errorf("got %+v, want batch myapp/invoke/ts=1718000000", p)
	}
}

func TestParseContainerName_FallbackWithoutKnownApps(t *testing.T) {
	SetKnownApps(nil)

	p, err := ParseContainerName("norn-myapp-web-0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.App != "myapp" || p.Process != "web" || p.Replica != 0 {
		t.Errorf("fallback parse got %+v, want myapp/web/0", p)
	}
}

func TestParseContainerName_HyphenatedAppWithKnownApps(t *testing.T) {
	SetKnownApps([]string{"field-harbor", "signal-sideband"})
	defer SetKnownApps(nil)

	p, err := ParseContainerName("norn-field-harbor-web-0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.App != "field-harbor" || p.Process != "web" {
		t.Errorf("got %+v, want field-harbor/web", p)
	}
}

func TestParseContainerName_HyphenatedProcessWithKnownApps(t *testing.T) {
	SetKnownApps([]string{"myapp"})
	defer SetKnownApps(nil)

	p, err := ParseContainerName("norn-myapp-cron-backup-cron-1718000000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.App != "myapp" || p.Process != "cron-backup" || p.Kind != "cron" {
		t.Errorf("got %+v, want myapp/cron-backup/cron", p)
	}
}

func TestParseContainerName_Invalid(t *testing.T) {
	tests := []string{
		"docker-something",
		"norn",
		"",
		"norn-",
		"norn-x",
	}
	for _, name := range tests {
		_, err := ParseContainerName(name)
		if err == nil {
			t.Errorf("ParseContainerName(%q) should have returned error", name)
		}
	}
}

func TestShortID(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"norn-myapp-web-0", "norn-mya"},
		{"short", "short"},
		{"12345678", "12345678"},
		{"123456789", "12345678"},
	}
	for _, tt := range tests {
		got := ShortID(tt.input)
		if got != tt.want {
			t.Errorf("ShortID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	SetKnownApps([]string{"myapp"})
	defer SetKnownApps(nil)

	// Service
	name := ContainerName("myapp", "web", 2)
	p, err := ParseContainerName(name)
	if err != nil {
		t.Fatal(err)
	}
	if p.App != "myapp" || p.Process != "web" || p.Replica != 2 || p.Kind != "service" {
		t.Errorf("service round-trip: %+v", p)
	}

	// Canary
	name = CanaryName("myapp", "web", 0)
	p, err = ParseContainerName(name)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != "canary" || p.Replica != 0 {
		t.Errorf("canary round-trip: %+v", p)
	}

	// Cron
	name = CronRunName("myapp", "backup", 1718000000)
	p, err = ParseContainerName(name)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != "cron" || p.Ts != 1718000000 {
		t.Errorf("cron round-trip: %+v", p)
	}

	// Batch
	name = BatchName("myapp", "invoke", 1718000000)
	p, err = ParseContainerName(name)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != "batch" || p.Ts != 1718000000 {
		t.Errorf("batch round-trip: %+v", p)
	}
}
