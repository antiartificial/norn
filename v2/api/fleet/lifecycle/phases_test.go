package lifecycle

import (
	"reflect"
	"testing"
)

func TestPhasesPerProfile(t *testing.T) {
	t.Parallel()
	if got := Phases(PostgresLegacy, false); !reflect.DeepEqual(got, legacyNoDrainPhases) {
		t.Fatalf("PostgresLegacy no-drain = %v, want %v", got, legacyNoDrainPhases)
	}
	if got := Phases(PostgresLegacy, false); len(got) != 6 {
		t.Fatalf("PostgresLegacy no-drain has %d phases, want 6", len(got))
	}
	if got := Phases(PostgresLegacy, true); !reflect.DeepEqual(got, canonicalPhases) {
		t.Fatalf("PostgresLegacy drain = %v, want canonical", got)
	}
	if got := Phases(V3, false); !reflect.DeepEqual(got, canonicalPhases) || len(got) != 9 {
		t.Fatalf("V3 no-drain = %v, want 9-phase canonical", got)
	}
	if got := Phases(V3, true); !reflect.DeepEqual(got, canonicalPhases) {
		t.Fatalf("V3 drain = %v, want canonical", got)
	}
}

func TestNextPhaseTerminal(t *testing.T) {
	t.Parallel()
	next, terminal, err := NextPhase(PostgresLegacy, "infrastructure_applied", false)
	if err != nil || terminal || next != "inventory_generated" {
		t.Fatalf("NextPhase(legacy, infrastructure_applied, false) = (%q,%v,%v)", next, terminal, err)
	}
	next, terminal, err = NextPhase(PostgresLegacy, "complete", false)
	if err != nil || !terminal || next != "complete" {
		t.Fatalf("NextPhase at terminal = (%q,%v,%v), want (complete,true,nil)", next, terminal, err)
	}
	next, terminal, err = NextPhase(V3, "old_nodes_drained", true)
	if err != nil || terminal || next != "complete" {
		t.Fatalf("NextPhase(V3, old_nodes_drained, true) = (%q,%v,%v)", next, terminal, err)
	}
	if _, _, err := NextPhase(PostgresLegacy, "not_a_phase", false); err == nil {
		t.Fatal("NextPhase with an unknown phase should error")
	}
}

func TestRequiresDrainTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		action        string
		current       float64
		proposed      float64
		requiresDrain bool
	}{
		{"replace always drains", "replace", 3, 3, true},
		{"scale down drains", "scale", 5, 2, true},
		{"scale up does not drain", "scale", 2, 5, false},
		{"scale equal does not drain", "scale", 3, 3, false},
		{"unknown action does not drain", "rename", 3, 2, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RequiresDrain(c.action, c.current, c.proposed); got != c.requiresDrain {
				t.Fatalf("RequiresDrain(%q,%v,%v) = %v, want %v", c.action, c.current, c.proposed, got, c.requiresDrain)
			}
		})
	}
}
