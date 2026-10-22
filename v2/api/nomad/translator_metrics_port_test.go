package nomad

import (
	"testing"

	"norn/v2/api/model"
)

func TestTranslateReservesMetricsHostPort(t *testing.T) {
	for _, tc := range []struct {
		name     string
		hostPort int
		reserved bool
	}{{"fixed", 3200, true}, {"dynamic", 0, false}} {
		t.Run(tc.name, func(t *testing.T) {
			spec := &model.InfraSpec{App: "tempo", Processes: map[string]model.Process{
				"web": {Port: 4318, HostPort: 4318, Metrics: &model.MetricsSpec{Enabled: true, Port: 3200, HostPort: tc.hostPort}},
			}}
			network := mustTranslate(t, spec, "img", nil).TaskGroups[0].Networks[0]
			var reserved, dynamic bool
			for _, port := range network.ReservedPorts {
				if port.Label == "web-metrics" && port.Value == 3200 && port.To == 3200 {
					reserved = true
				}
			}
			for _, port := range network.DynamicPorts {
				if port.Label == "web-metrics" && port.To == 3200 {
					dynamic = true
				}
			}
			if reserved != tc.reserved || dynamic == tc.reserved {
				t.Fatalf("metrics port reserved=%v dynamic=%v; network %+v", reserved, dynamic, network)
			}
		})
	}
}
