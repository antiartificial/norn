package etcdstore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"norn/v2/api/effect"
	"norn/v2/api/model"
)

type workloadReleaseEffectDescriptor struct {
	Intent json.RawMessage `json:"intent"`
}

type workloadReleaseEffectIdentity struct {
	ID     string `json:"id"`
	App    string `json:"app"`
	Region string `json:"region"`
}

func validateWorkloadReleaseEffectReservation(r effect.Reservation, op model.Operation, authority string) (string, error) {
	if r.Authority != authority || r.OperationClaim.OperationID != op.ID || r.OperationClaim.OwnerID == "" || r.OperationClaim.Generation <= 0 ||
		r.Stage != "app.workload-release.stop-old.nomad" || r.Supervisor != "nomad-workload-release-stop" || r.SupervisorExecutionID == "" || op.Kind != "app.workload-release" {
		return "", fmt.Errorf("workload release effect reservation does not match accepted operation and authority")
	}
	var descriptor workloadReleaseEffectDescriptor
	if err := json.Unmarshal(r.LaunchPayload, &descriptor); err != nil || !json.Valid(descriptor.Intent) {
		return "", fmt.Errorf("decode workload release effect descriptor")
	}
	var intent, accepted workloadReleaseEffectIdentity
	if err := json.Unmarshal(descriptor.Intent, &intent); err != nil {
		return "", fmt.Errorf("decode workload release intent identity")
	}
	acceptedRaw := effectPayloadString(op.Payload, "workloadReleaseIntent")
	if acceptedRaw == "" || json.Unmarshal([]byte(acceptedRaw), &accepted) != nil || intent.ID == "" || intent.App == "" || intent.Region == "" ||
		accepted != intent || intent.App != op.App || r.Resource != "app/"+intent.App+"/workload-release/"+intent.Region ||
		strings.Contains(intent.App, "/") || strings.Contains(intent.Region, "/") {
		return "", fmt.Errorf("workload release effect intent differs from accepted operation")
	}
	var acceptedValue, intentValue interface{}
	if json.Unmarshal([]byte(acceptedRaw), &acceptedValue) != nil || json.Unmarshal(descriptor.Intent, &intentValue) != nil {
		return "", fmt.Errorf("workload release intent JSON is invalid")
	}
	acceptedCanonical, err := json.Marshal(acceptedValue)
	if err != nil {
		return "", err
	}
	intentCanonical, err := json.Marshal(intentValue)
	if err != nil || !bytes.Equal(acceptedCanonical, intentCanonical) {
		return "", fmt.Errorf("workload release effect intent bytes differ from accepted operation")
	}
	digest, err := effect.ComputeInputDigest(r)
	if err != nil || r.InputDigest != digest {
		return "", fmt.Errorf("workload release effect input digest does not match reservation")
	}
	return intent.App, nil
}
