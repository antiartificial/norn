package handler

import (
	"testing"
	"time"

	"norn/v2/api/model"
)

func TestCronParentJobID(t *testing.T) {
	tests := []struct {
		name    string
		app     string
		process string
		jobID   string
		want    string
	}{
		{
			name:  "child job",
			jobID: "field-harbor-field-harbor-sync-pm/periodic-1781658600",
			want:  "field-harbor-field-harbor-sync-pm",
		},
		{
			name:  "parent job",
			jobID: "field-harbor-field-harbor-sync-pm",
			want:  "field-harbor-field-harbor-sync-pm",
		},
		{
			name:    "app and process",
			app:     "field-harbor",
			process: "field-harbor-sync-pm",
			want:    "field-harbor-field-harbor-sync-pm",
		},
		{
			name: "missing evidence",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cronParentJobID(tt.app, tt.process, tt.jobID); got != tt.want {
				t.Fatalf("cronParentJobID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMetadataString(t *testing.T) {
	metadata := map[string]interface{}{
		"process": "field-harbor-sync-pm",
		"attempt": 2,
	}
	if got := metadataString(metadata, "process"); got != "field-harbor-sync-pm" {
		t.Fatalf("metadataString(process) = %q", got)
	}
	if got := metadataString(metadata, "attempt"); got != "2" {
		t.Fatalf("metadataString(attempt) = %q", got)
	}
	if got := metadataString(metadata, "missing"); got != "" {
		t.Fatalf("metadataString(missing) = %q", got)
	}
}

func TestTaskRestartStabilityWindow(t *testing.T) {
	now := time.Now()
	if taskRestartStable(now.Add(-14*time.Minute), now) {
		t.Fatal("recent restart must remain open during the stability window")
	}
	if !taskRestartStable(now.Add(-16*time.Minute), now) {
		t.Fatal("healthy restart older than the stability window should reconcile")
	}
	if taskRestartStable(time.Time{}, now) || taskRestartStable(now.Add(time.Minute), now) {
		t.Fatal("missing or future restart timestamps must not reconcile")
	}
}

func TestCapacityWarningRecoveryEvidence(t *testing.T) {
	now := time.Now().UTC()
	warning := model.BeaconEvent{
		ID:          "legacy-warning",
		Type:        "service.capacity.below_minimum",
		Source:      "norn-mini",
		App:         hostCapacityEventApp,
		Environment: "development",
		OccurredAt:  now,
		Metadata:    map[string]interface{}{"correlationKey": hostCapacityCorrelationKey},
	}

	tests := []struct {
		name     string
		recovery *model.BeaconEvent
		want     bool
	}{
		{
			name: "later aggregate capacity recovery",
			recovery: &model.BeaconEvent{
				ID:          "capacity-recovery",
				Type:        "service.capacity.recovered",
				Severity:    model.BeaconInfo,
				Source:      "norn-mini",
				App:         hostCapacityEventApp,
				Environment: "development",
				OccurredAt:  now.Add(time.Minute),
				Metadata:    map[string]interface{}{"correlationKey": hostCapacityCorrelationKey, "legacyCapacityWarningID": warning.ID},
			},
			want: true,
		},
		{
			name: "generic legacy recovery cannot close global warning",
			recovery: &model.BeaconEvent{
				Type:        "service.capacity.recovered",
				Severity:    model.BeaconInfo,
				Source:      "norn-mini",
				App:         hostCapacityEventApp,
				Environment: "development",
				OccurredAt:  now.Add(time.Minute),
				Metadata:    map[string]interface{}{"correlationKey": hostCapacityCorrelationKey},
			},
			want: false,
		},
		{
			name: "host assurance recovery is not capacity recovery",
			recovery: &model.BeaconEvent{
				Type:        "host.assurance.recovered",
				Severity:    model.BeaconInfo,
				Source:      "norn-mini",
				App:         hostCapacityEventApp,
				Environment: "development",
				OccurredAt:  now.Add(time.Minute),
				Metadata:    map[string]interface{}{"correlationKey": "norn-host:assurance"},
			},
			want: false,
		},
		{
			name: "app scoped recovery cannot close host aggregate",
			recovery: &model.BeaconEvent{
				Type:        "service.capacity.recovered",
				Severity:    model.BeaconInfo,
				Source:      "norn-mini",
				App:         "turnkey-offer-intake",
				Environment: "development",
				OccurredAt:  now.Add(time.Minute),
				Metadata:    map[string]interface{}{"correlationKey": hostCapacityCorrelationKey},
			},
			want: false,
		},
		{
			name: "recovery from another environment cannot close warning",
			recovery: &model.BeaconEvent{
				Type:        "service.capacity.recovered",
				Severity:    model.BeaconInfo,
				Source:      "norn-mini",
				App:         hostCapacityEventApp,
				Environment: "production",
				OccurredAt:  now.Add(time.Minute),
				Metadata:    map[string]interface{}{"correlationKey": hostCapacityCorrelationKey},
			},
			want: false,
		},
		{
			name: "recovery from another source cannot close warning",
			recovery: &model.BeaconEvent{
				Type:        "service.capacity.recovered",
				Severity:    model.BeaconInfo,
				Source:      "another-host",
				App:         hostCapacityEventApp,
				Environment: "development",
				OccurredAt:  now.Add(time.Minute),
				Metadata:    map[string]interface{}{"correlationKey": hostCapacityCorrelationKey},
			},
			want: false,
		},
		{
			name: "recovery at warning time cannot close warning",
			recovery: &model.BeaconEvent{
				Type:        "service.capacity.recovered",
				Severity:    model.BeaconInfo,
				Source:      "norn-mini",
				App:         hostCapacityEventApp,
				Environment: "development",
				OccurredAt:  now,
				Metadata:    map[string]interface{}{"correlationKey": hostCapacityCorrelationKey},
			},
			want: false,
		},
		{
			name: "warning recovery event cannot close warning",
			recovery: &model.BeaconEvent{
				Type:        "service.capacity.recovered",
				Severity:    model.BeaconWarning,
				Source:      "norn-mini",
				App:         hostCapacityEventApp,
				Environment: "development",
				OccurredAt:  now.Add(time.Minute),
				Metadata:    map[string]interface{}{"correlationKey": hostCapacityCorrelationKey},
			},
			want: false,
		},
		{
			name:     "no recovery",
			recovery: nil,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := capacityWarningSupersededBy(warning, tt.recovery); got != tt.want {
				t.Fatalf("capacityWarningSupersededBy() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestCapacityWarningScopeRejectsAppScopedAndUncorrelatedEvents(t *testing.T) {
	tests := []model.BeaconEvent{
		{App: "turnkey-offer-intake", Metadata: map[string]interface{}{"correlationKey": hostCapacityCorrelationKey}},
		{App: hostCapacityEventApp, Metadata: map[string]interface{}{"correlationKey": "app:turnkey-offer-intake"}},
	}
	for _, event := range tests {
		if capacityWarningScopeError(event) == "" {
			t.Fatalf("capacityWarningScopeError(%#v) accepted an unsafe scope", event)
		}
	}
}

func TestCapacityWarningScopedIdentityDoesNotCrossHosts(t *testing.T) {
	now := time.Now().UTC()
	warning := model.BeaconEvent{
		ID: "host-a-warning", Type: "service.capacity.below_minimum", Severity: model.BeaconWarning,
		Source: "norn-host:host-a", App: hostCapacityEventApp, Environment: "development", OccurredAt: now,
		Metadata: map[string]interface{}{"correlationKey": "norn-host:host-a:minimum-capacity", "hostScope": "host-a"},
	}
	matchingRecovery := model.BeaconEvent{
		Type: "service.capacity.recovered", Severity: model.BeaconInfo,
		Source: "norn-host:host-a", App: hostCapacityEventApp, Environment: "development", OccurredAt: now.Add(time.Minute),
		Metadata: map[string]interface{}{"correlationKey": "norn-host:host-a:minimum-capacity", "hostScope": "host-a"},
	}
	if !capacityWarningSupersededBy(warning, &matchingRecovery) {
		t.Fatal("matching host-scoped recovery did not close its warning")
	}
	otherHost := matchingRecovery
	otherHost.Source = "norn-host:host-b"
	otherHost.Metadata = map[string]interface{}{"correlationKey": "norn-host:host-b:minimum-capacity", "hostScope": "host-b"}
	if capacityWarningSupersededBy(warning, &otherHost) {
		t.Fatal("a recovery from another physical host closed host-a's warning")
	}

	badMetadata := warning
	badMetadata.Metadata = map[string]interface{}{"correlationKey": "norn-host:host-a:minimum-capacity", "hostScope": "host-b"}
	if capacityWarningScopeError(badMetadata) == "" {
		t.Fatal("a mismatched metadata host scope was accepted")
	}
}

func TestLegacyCapacityAdoptionOnlySupersedesItsIdentifiedWarning(t *testing.T) {
	now := time.Now().UTC()
	warning := model.BeaconEvent{
		ID: "legacy-warning-a", Type: "service.capacity.below_minimum", Source: "norn", App: hostCapacityEventApp,
		Environment: "development", OccurredAt: now, Metadata: map[string]interface{}{"correlationKey": hostCapacityLegacyCorrelationKey},
	}
	recovery := model.BeaconEvent{
		Type: "service.capacity.recovered", Severity: model.BeaconInfo, Source: "norn", App: hostCapacityEventApp,
		Environment: "development", OccurredAt: now.Add(time.Minute),
		Metadata: map[string]interface{}{"correlationKey": hostCapacityLegacyCorrelationKey, "legacyCapacityWarningID": warning.ID},
	}
	if !capacityWarningSupersededBy(warning, &recovery) {
		t.Fatal("identified legacy warning was not superseded by its adoption")
	}
	warning.ID = "legacy-warning-b"
	if capacityWarningSupersededBy(warning, &recovery) {
		t.Fatal("one-time legacy adoption superseded a different legacy warning")
	}
}
