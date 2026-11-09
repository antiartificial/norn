export const title = "Mixed application version resize (proposed contract)";
export const colors = { planning: "blue", rollout: "teal", held: "orange", done: "purple" };
export const state = (v: any) => {
  if (v.routeEvidenceRecorded && !v.routeReceipt &&
      ["mixed", "draining", "observing", "contracted", "held"].includes(v.phase)) {
    return "Route evidence stale, rollout held";
  }
  if (v.phase === "current") return "Old version serving";
  if (v.phase === "planned") return "Exact intent admitted";
  if (v.phase === "expanded") return "Additive schema ready";
  if (v.phase === "warming") return "Target cohort warming";
  if (v.phase === "mixed") return v.route === "target" ? "Target route, old cohort draining" : "Canary route, versions overlap";
  if (v.phase === "draining") return "Drain requested, awaiting acknowledgement";
  if (v.phase === "recovering") return "Reconcile same durable intent";
  if (v.phase === "observing") return "Target serving, old writers quiesced";
  if (v.phase === "contracted") return "Old schema contract retired";
  if (v.phase === "complete") return "Resize and release complete";
  if (v.phase === "held") return v.intent === "rejected" ? "Intent rejected, no effect" : "Failure held for reconciliation";
  return "Unknown state";
};
export const group = (v: any) => {
  if (v.phase === "held" || v.phase === "recovering" ||
      (v.routeEvidenceRecorded && !v.routeReceipt)) return "held";
  if (v.phase === "complete" || v.phase === "contracted") return "done";
  if (v.phase === "warming" || v.phase === "mixed" || v.phase === "draining" || v.phase === "observing") return "rollout";
  return "planning";
};
export const step = (action: string, from: any, to: any) => {
  // Keep representative failures and recovery visible. TLC still checks crash
  // and route failure from every phase in the unchanged full model.
  if (action === "ControllerCrash" && from.phase !== "current") return null;
  if (action === "RouteEvidenceLost" && !(from.phase === "mixed" && from.route === "mixed")) return null;
  if (action === "ReconcileExactState" && to.phase !== "planned") return null;
  return ({
    AdmitIntent: "accept one exact signed target",
    RejectBadIntent: "reject a stale or incompatible intent",
    ExpandSchema: "expand the schema while both versions remain compatible",
    StartTarget: "start target allocations within surge capacity",
    TargetReady: "target health passes",
    PublishMixedRoute: "publish a canary route and retain its receipt",
    AcknowledgeDrain: "workload confirms drain completion",
    RetireOld: "retire one old allocation after route and drain evidence",
    PublishTargetRoute: "route all traffic to the ready target cohort",
    QuiesceOldWriters: "prove old writers are stopped",
    ContractSchema: "retire old schema only after old writers exit",
    Complete: "record terminal target receipt",
    TargetFails: "target health fails, keep old cohort and hold",
    TargetAllocationLost: "one canary allocation is lost, hold before draining old version",
    RouteEvidenceLost: "route proof is lost, stop further effects",
    ReconfirmCurrentRoute: "fresh readback confirms the exact current route",
    ControllerCrash: "controller stops with durable intent retained",
    ResumeSameIntent: "restart under the same intent",
    ReconcileExactState: "reconcile observed allocations before resuming",
  } as Record<string, string>)[action] ?? null;
};
