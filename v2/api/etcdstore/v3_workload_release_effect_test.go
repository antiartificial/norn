package etcdstore

import (
	"encoding/json"
	"testing"

	"norn/v2/api/effect"
	"norn/v2/api/model"
)

func TestWorkloadReleaseEffectReservationMatchesAcceptedIntent(t *testing.T) {
	acceptedIntent := json.RawMessage(`{"id":"release-1","app":"widgets","region":"nyc3","routeSteps":[{"oldWeight":100,"targetWeight":0},{"oldWeight":50,"targetWeight":50}]}`)
	op := model.Operation{ID: "operation-1", Kind: "app.workload-release", App: "widgets", Payload: map[string]interface{}{"workloadReleaseIntent": string(acceptedIntent)}}
	reservation := effect.Reservation{
		Authority: "authority-1", Resource: "app/widgets/workload-release/nyc3",
		OperationClaim: effect.OperationClaim{OperationID: op.ID, OwnerID: "worker-1", Generation: 1},
		Stage:          "app.workload-release.stop-old.nomad", Supervisor: "nomad-workload-release-stop",
		SupervisorExecutionID: "execution-1",
		LaunchPayload:         json.RawMessage(`{"intent":{"id":"release-1","app":"widgets","region":"nyc3","routeSteps":[{"oldWeight":100,"targetWeight":0},{"oldWeight":50,"targetWeight":50}]}}`),
	}
	var err error
	reservation.InputDigest, err = effect.ComputeInputDigest(reservation)
	if err != nil {
		t.Fatal(err)
	}
	if app, err := validateWorkloadReleaseEffectReservation(reservation, op, "authority-1"); err != nil || app != "widgets" {
		t.Fatalf("valid accepted intent app=%q err=%v", app, err)
	}

	tampered := op
	tampered.Payload = map[string]interface{}{"workloadReleaseIntent": `{"id":"release-1","app":"widgets","region":"nyc3","routeSteps":[{"oldWeight":100,"targetWeight":0},{"oldWeight":25,"targetWeight":75}]}`}
	if _, err := validateWorkloadReleaseEffectReservation(reservation, tampered, "authority-1"); err == nil {
		t.Fatal("effect descriptor for changed route step matched signed accepted intent")
	}

	for name, mutate := range map[string]func(*effect.Reservation){
		"wrong resource":       func(r *effect.Reservation) { r.Resource = "app/widgets/workload-release/sfo3" },
		"wrong authority":      func(r *effect.Reservation) { r.Authority = "authority-2" },
		"wrong stage":          func(r *effect.Reservation) { r.Stage = "app.scale.nomad" },
		"wrong payload digest": func(r *effect.Reservation) { r.InputDigest = "sha256:" + string(make([]byte, 64)) },
	} {
		t.Run(name, func(t *testing.T) {
			copy := reservation
			mutate(&copy)
			if _, err := validateWorkloadReleaseEffectReservation(copy, op, "authority-1"); err == nil {
				t.Fatal("invalid workload release effect reservation accepted")
			}
		})
	}
}
