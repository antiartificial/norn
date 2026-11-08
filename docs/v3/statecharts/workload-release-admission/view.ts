export const title = "V3 workload release: private staging and crash recovery";
export const state = (v: any) => {
  if (v.phase === "held") return v.job === "matching" ? "Held, late target may exist" : "Held, old service remains";
  const labels: Record<string, string> = {
    accepted: "Accepted, old service active",
    "attempt-marked": "Create permission recorded",
    submitting: "Target create in flight",
    submitted: "Private target created",
    ambiguous: "Create response lost",
    bound: "Exact target revision bound",
    warming: "Target warming, old route active",
    "old-only": "Old route proven",
    mixed: "Traffic shifting, old task retained",
    "target-only": "Target route active, old task retained",
    draining: "Fresh target-only proof",
    complete: "Target serving, old job stopped"
  };
  return labels[v.phase] ?? v.phase;
};
export const step = (action: string) => ({
  ResolvePinnedInputs: "resolve immutable encrypted input revision",
  RotateCurrentSecret: "rotate current secret pointer; accepted plan stays pinned",
  LosePinnedSecretSnapshot: "pinned encrypted input becomes unavailable",
  HoldOnMissingPinnedInputs: "hold before scheduler submission",
  MarkAttempt: "record create permission",
  SendCreate: "send create-only request",
  NomadAccepts: "scheduler accepts target",
  LoseResponse: "response is lost",
  CrashAndReclaim: "worker restarts under successor claim",
  ObserveMissingAfterAttempt: "successor sees no exact target [hold]",
  ObserveMatchingJob: "successor binds exact target readback",
  ObserveMismatch: "successor sees a different job [hold]",
  TargetBecomesHealthy: "target reaches full health",
  ProveOldOnly: "prove current old-only route",
  PublishMixed: "publish admitted mixed route",
  PublishTargetOnly: "publish target-only route",
  RefreshTargetOnlyProof: "refresh target health and route proof",
  TargetLosesHealth: "target health is lost [hold]",
  StopOld: "stop old job after fresh proof",
  Hold: "evidence becomes uncertain [hold]",
  LateAcceptWhileHeld: "delayed create arrives after hold",
  Done: null
} as Record<string, string | null>)[action] ?? action;
export const group = (v: any) => v.phase === "held" ? "held" : v.phase === "complete" ? "done" : "active";
export const colors = { active: "blue", held: "orange", done: "teal" };
