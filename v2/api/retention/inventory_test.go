package retention

import (
	"testing"

	"norn/v2/api/controlrecovery"
)

// Every control table has exactly one retention decision, and every named
// bulky column exists, so new tables or payload columns cannot silently
// escape the inventory.
func TestPayloadInventoryCoversEveryControlTable(t *testing.T) {
	registry := map[string]map[string]bool{}
	for _, table := range controlrecovery.InspectionRegistry() {
		columns := map[string]bool{}
		for _, column := range table.Columns {
			columns[column.Name] = true
		}
		registry[table.Name] = columns
	}
	classes := map[string]bool{ClassArchived: true, ClassHotEvidence: true, ClassExpiring: true, ClassAgePolicy: true, ClassCurrentState: true}
	seen := map[string]bool{}
	for _, entry := range PayloadInventory {
		columns, ok := registry[entry.Table]
		if !ok {
			t.Errorf("inventory names unknown table %s", entry.Table)
			continue
		}
		if seen[entry.Table] {
			t.Errorf("table %s is classified twice", entry.Table)
		}
		seen[entry.Table] = true
		if !classes[entry.Class] || entry.Holds == "" {
			t.Errorf("table %s has no valid class or holds", entry.Table)
		}
		for _, column := range entry.Bulky {
			if !columns[column] {
				t.Errorf("table %s has no bulky column %s", entry.Table, column)
			}
		}
	}
	for table := range registry {
		if !seen[table] {
			t.Errorf("control table %s has no retention decision", table)
		}
	}
}
