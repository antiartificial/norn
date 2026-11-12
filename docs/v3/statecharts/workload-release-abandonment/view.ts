export const title = "Held workload release: abandon safely and retain shared keys";
export const state = (v: any) => {
  if (!v.keyAvailable) return (v.inputPresent || v.otherInputPresent)
    ? "Key retired while retained input still exists"
    : "No retained input references; key retired";
  if (!v.inputPresent && !v.otherInputPresent) {
    return v.keyReferenceProof ? "No retained key references; safe to retire key" : "Inputs tombstoned; key-reference proof pending";
  }
  if (v.phase === "Abandoned") {
    if (!v.effectsReconciled || !v.abandonAuthorized) return "Abandonment recorded without safe readback or authority";
    return v.inputPresent ? "Abandonment receipt durable; inputs retained" : "Abandoned input deleted; shared key still pinned";
  }
  if (v.phase === "Complete") return v.inputPresent ? "Completion receipt durable; inputs retained" : "Completed input deleted; shared key still pinned";
  if (v.phase === "Held" && !v.inputPresent) return "Held; input deleted before terminal disposition";
  if (v.phase === "Held" && !v.effectsReconciled) return v.owner === "NoOwner" ? "Held; worker lost; effects unresolved; input retained" : "Held; effects unresolved; input retained";
  if (v.phase === "Held" && v.effectsReconciled && !v.abandonAuthorized) return "Held; effects read back; explicit authority pending";
  if (v.phase === "Held" && v.abandonAuthorized) return "Held; abandonment authorized; receipt pending";
  if (v.owner === "NoOwner") return "Release active; worker lost; input retained";
  return "Release active; input retained";
};
export const step = (action: string) => ({
  HoldRelease: "release enters a durable hold",
  LoseWorker: "worker lease is lost",
  SuccessorClaimsReadback: "successor claims read-only reconciliation",
  ReconcileExternalEffects: "readback proves external effects settled",
  AuthorizeAbandonment: "owner signs explicit abandonment authority",
  AbandonRelease: "controller records the abandonment receipt",
  CompleteRelease: "controller records the complete release receipt",
  DeleteInputsAfterDisposition: "delete this input and write its receipt-bound tombstone",
  UnsafeDeleteHeldInputs: "unsafe path deletes inputs while the operation remains held",
  CompleteOtherRecord: "another operation deletes its input with a terminal receipt",
  ProveNoKeyReferences: "controller verifies no retained record uses this key",
  RetireUnreferencedKey: "retire the now-unreferenced key version",
  UnsafeRetireSharedKey: "unsafe path retires a key still needed by an input",
} as Record<string, string>)[action] ?? null;
export const colors = { Work: "blue", Held: "orange", Terminal: "teal", Retired: "purple" };
export const group = (v: any) => !v.keyAvailable ? "Retired" : v.phase === "Held" ? "Held" : ["Complete", "Abandoned"].includes(v.phase) ? "Terminal" : "Work";
