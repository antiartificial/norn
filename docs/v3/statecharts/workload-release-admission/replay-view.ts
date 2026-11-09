export const title = "V3 workload admission: replay after response loss and restart";

export const state = (v: any) => {
  if (!v.controllerAlive) return "Controller stopped; durable receipt remains";
  if (v.replayResult === "original receipt") return "Original durable receipt returned";
  if (v.replayResult === "conflict") return "Changed qualification rejected";
  if (v.durableReceipt !== "none" && !v.processLocalAcceptance)
    return "Controller restarted; loading durable acceptance";
  if (v.durableReceipt !== "none" && !v.initialResponseObserved)
    return "Acceptance committed; response lost";
  if (v.phase === "request") return "Qualification request received";
  if (v.phase === "rejected") return "Requested size rejected by app policy";
  if (v.selectedQualification !== v.acceptedQualification || v.selectedReplicas !== v.acceptedReplicas)
    return "Retry changes qualification or target size";
  if (!v.qualificationValid && !v.sourceAvailable)
    return "Accepted qualification expired, source unavailable";
  if (!v.qualificationValid) return "Accepted qualification expired";
  if (!v.sourceAvailable) return "Accepted deployment source unavailable";
  return "Accepted qualification and source still available";
};

export const step = (action: string, _from: any, to: any) => {
  if (action === "CommitVerifiedRequest")
    return to.initialResponseObserved
      ? "persist verified acceptance and return its receipt"
      : "persist verified acceptance but lose the HTTP response";
  const labels: Record<string, string> = {
    RejectOutOfPolicySize: "reject a target size outside the operator-approved range",
    SelectOutOfPolicyReplicaCount: "request a target size outside the operator-approved range",
    QualificationExpires: "qualification expires after acceptance",
    AcceptedSourceDisappears: "source deployment becomes unavailable",
    ControllerCrashes: "controller stops after the durable commit",
    ControllerRestarts: "new process starts without local acceptance memory",
    RetrySameQualification: "retry with the same idempotency key and request",
    SelectDifferentQualification: "retry changes the selected qualification",
    SelectDifferentReplicaCount: "retry changes the target replica count",
    RetryChangedQualification: "reject changed qualification or size without rereading source",
    Done: "return the terminal replay result",
  };
  return labels[action] ?? action;
};

export const group = (v: any) =>
  !v.controllerAlive ? "recovering" :
  v.replayResult === "conflict" || v.phase === "rejected" ? "rejected" :
  v.replayResult === "original receipt" ? "replayed" :
  v.phase === "accepted" ? "accepted" : "request";

export const colors = { request: "blue", accepted: "purple", recovering: "orange", replayed: "teal", rejected: "tan" };
