export const title = "V3 workload release: private staging and crash recovery";
export const state = (v: any) => {
  if (v.replayReturnedOriginal) return "Original acceptance replayed";
  if (v.phase === "held") {
    if (!v.inputKeyAvailable && !v.binding)
      return v.job === "matching"
        ? "Held, key unavailable and delayed target may exist"
        : "Held, release key unavailable with old service remaining";
    if (v.job === "foreign") return "Held, different target found with old service remaining";
    if (v.binding && !v.targetHealthy) return "Held, target health lost with old route remaining";
    if (v.job === "matching" && !v.binding) return "Held, late target may exist";
    return "Held, old service remains";
  }
  const labels: Record<string, string> = {
    request: "Release request received",
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
export const step = (action: string, from: any, to: any) => {
  if (!from.replayReturnedOriginal && to.replayReturnedOriginal)
    return "same request key returns the original accepted operation";
  if (from.inputKeyAvailable && !to.inputKeyAvailable)
    return "historical envelope key is missing on recovery, hold before binding";
  if (from.secretSnapshotAvailable && !to.secretSnapshotAvailable)
    return "pinned encrypted input becomes unavailable";
  if (from.currentSecretRevision !== to.currentSecretRevision)
    return "current secret rotates while accepted release stays pinned";
  if (from.claim !== to.claim) return "worker restarts under successor claim";
  if (to.job === "foreign" && from.job !== to.job)
    return "successor finds a different target and holds";
  if (from.phase === "held" && to.phase === "held" && from.job !== to.job)
    return "delayed target registration arrives after hold";
  if (from.phase !== to.phase) {
    const transitions: Record<string, string> = {
      "accepted:attempt-marked": "record create permission",
      "request:accepted": "verify signed plan and reserve app and route",
      "attempt-marked:submitting": "send create-only request",
      "submitting:submitted": "scheduler accepts target",
      "submitting:ambiguous": "create response is lost",
      "ambiguous:held": "successor sees no exact target and holds",
      "submitted:bound": "successor binds exact target readback",
      "ambiguous:bound": "successor binds exact target readback",
      "attempt-marked:bound": "successor binds exact target readback",
      "submitting:bound": "successor binds exact target readback",
      "bound:warming": "target reaches full health",
      "warming:old-only": "prove current old-only route",
      "old-only:mixed": "publish admitted mixed route",
      "mixed:target-only": "publish target-only route",
      "target-only:draining": "refresh target health and route proof",
      "draining:complete": "stop old job after fresh proof",
      "warming:held": "target health or evidence is lost and release holds",
      "old-only:held": "target health or evidence is lost and release holds",
      "mixed:held": "target health or evidence is lost and release holds",
      "target-only:held": "target health or evidence is lost and release holds",
      "draining:held": "target health or evidence is lost and release holds",
      "accepted:held": "required private input is unavailable and release holds",
      "attempt-marked:held": "required private input is unavailable and release holds",
      "submitting:held": "required private input is unavailable and release holds",
      "submitted:held": "required private input is unavailable and release holds",
      "ambiguous:held": "required private input is unavailable and release holds"
    };
    return transitions[`${from.phase}:${to.phase}`] ?? `${to.phase} while preserving the old service`;
  }
  if (action === "Next") return "controller reconciles the same accepted release";
  return action;
};
export const group = (v: any) => v.phase === "held" ? "held" : v.phase === "complete" ? "done" : "active";
export const colors = { active: "blue", held: "orange", done: "teal" };
