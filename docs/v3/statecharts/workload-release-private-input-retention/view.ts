export const title = "Private workload input retention and recovery";
export const state = (v: any) => {
  if (v.phase === "Complete" && !v.input && v.tombstone) return "Completed; inputs deleted";
  if (v.phase === "Complete") return "Release complete; inputs retained";
  if (v.owner === "NoOwner") return v.phase === "Held" ? "Held; worker lost" : "Pending; worker lost";
  if (v.phase === "Held") return "Held; inputs retained";
  return "Pending; inputs retained";
};
export const step = (action: string, from: any, to: any) => ({
  Hold: "evidence unavailable; preserve inputs",
  Recover: "successor resumes same release",
  CompleteRelease: "validated terminal receipt stored",
  DeleteInputs: "delete encrypted inputs and write tombstone",
  LoseWorker: "worker claim expires",
  SuccessorClaims: "successor obtains fenced claim",
  RetryDeletion: "reconcile completed deletion",
  Done: "terminal cleanup remains durable",
} as Record<string, string>)[action] ?? action;
