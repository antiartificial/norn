package appdelete

// App04Admission is a verifier-owned, one-use commitment to the reviewed
// provider and workload facts. It is not a provider operation or a grant.
const App04AdmissionSchema = "norn.fleet-app04-delete-admission/v1"
const App03AdmissionSchema = "norn.fleet-app03-delete-admission/v1"

type App04Admission struct {
	SchemaVersion         string `json:"schemaVersion"`
	PlanID                string `json:"planId"`
	AttemptID             string `json:"attemptId"`
	PlanSHA256            string `json:"planSha256"`
	SourceCommit          string `json:"sourceCommit"`
	TargetID              string `json:"targetId"`
	TargetFenceGeneration int64  `json:"targetFenceGeneration"`
	AuthorityEpoch        int64  `json:"authorityEpoch"`
	Target                string `json:"target"`
	DropletID             string `json:"dropletId"`
	ProviderAccount       string `json:"providerAccount"`
	SavedPlanSHA256       string `json:"savedPlanSha256"`
	WriterGeneration      uint64 `json:"writerGeneration"`
	WriterHolder          string `json:"writerHolder"`
	AuthorizationSHA256   string `json:"authorizationSha256"`
	WorkloadReceiptSHA256 string `json:"workloadReceiptSha256"`
	DeleteRequestSHA256   string `json:"deleteRequestSha256"`
}

// App03Admission uses the same sealed fields with an exact app03 schema and target.
type App03Admission = App04Admission

func (a App04Admission) Validate() error {
	request := Request{SchemaVersion: RequestSchema, PlanID: a.PlanID, AttemptID: a.AttemptID,
		WriterGeneration: a.WriterGeneration, WriterHolder: a.WriterHolder, Target: a.Target,
		DropletID: a.DropletID, ProviderAccount: a.ProviderAccount,
		AuthorizationSHA256: a.AuthorizationSHA256, WorkloadReceiptSHA256: a.WorkloadReceiptSHA256,
		DeleteRequestSHA256: a.DeleteRequestSHA256}
	if !((a.SchemaVersion == App04AdmissionSchema && a.Target == "app-04") ||
		(a.SchemaVersion == App03AdmissionSchema && a.Target == "app-03")) || request.Validate() != nil ||
		!hex64.MatchString(a.PlanSHA256) || !hex40.MatchString(a.SourceCommit) ||
		!hex64.MatchString(a.SavedPlanSHA256) || a.TargetID == "" ||
		a.TargetFenceGeneration <= 0 || a.AuthorityEpoch <= 0 {
		return ErrInvalid
	}
	return nil
}

func (a App04Admission) MatchesRequest(r Request) bool {
	return a.PlanID == r.PlanID && a.AttemptID == r.AttemptID && a.Target == r.Target &&
		a.DropletID == r.DropletID && a.ProviderAccount == r.ProviderAccount &&
		a.WriterGeneration == r.WriterGeneration && a.WriterHolder == r.WriterHolder &&
		a.AuthorizationSHA256 == r.AuthorizationSHA256 &&
		a.WorkloadReceiptSHA256 == r.WorkloadReceiptSHA256 && a.DeleteRequestSHA256 == r.DeleteRequestSHA256
}
