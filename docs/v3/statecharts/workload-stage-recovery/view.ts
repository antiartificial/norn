export const title = "Recovering a workload target after an uncertain create";
export const state = (v: any) => {
  if (v.terminal) return "Release complete";
  if (!v.controllerAlive) return "Controller stopped";
  if (!v.oldServing) return "Old workload drained; receipt pending";
  if (v.routeTarget) return "Target serving, old workload retained";
  if (v.targetHealthy) return "Target healthy; old route serving";
  if (v.targetBound) return "Exact target bound";
  if (v.targetObserved) return "Target read back";
  if (v.targetExists) return "Target created; readback pending";
  if (v.phase === "reconciling") return v.targetExists ? "Reconciling existing target" : "Target not yet visible";
  if (v.createPending) return "Create response uncertain";
  if (v.marker) return "Create attempt durably recorded";
  return ({accepted:"Intent accepted",claimed:"Operation claimed",reserved:"Target effect reserved","inputs-staged":"Private inputs staged"} as any)[v.phase] ?? v.phase;
};
export const step = (action: string) => ({
  ClaimOperation:"controller claims the accepted intent",
  ReserveTargetEffect:"controller reserves the target effect",
  StagePrivateInputs:"controller stages the exact encrypted inputs",
  RecordCreateAttempt:"controller records the one-use create marker",
  SubmitCreate:"controller submits one create-only request",
  NomadAcceptsCreate:"Nomad accepts the delayed create",
  ControllerCrashes:"controller process stops",
  SuccessorClaims:"successor claims the same intent",
  ReadExactTarget:"successor reads the exact target",
  ReadTargetAbsent:"readback still cannot find the target",
  BindObservedTarget:"successor binds the exact readback",
  ObserveHealthyTarget:"controller verifies target health",
  PublishTargetRoute:"controller publishes the target route",
  DrainOldAfterTargetRoute:"controller drains the old workload after target-only proof",
  CompleteRelease:"controller records completion",
}[action] ?? null);
