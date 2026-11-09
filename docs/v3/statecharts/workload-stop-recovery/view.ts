export const title = "Old deployment stop and lost-response recovery";
export const colors = { planned: "blue", stopped: "teal", recovery: "orange", done: "purple" };
export const state = (v: any) => {
  if (v.phase === "planned") return "Intent pins the old deployment";
  if (v.phase === "reserved") return "One stop effect reserved";
  if (v.phase === "awaiting-response") return "Nomad accepted one guarded stop";
  if (v.phase === "recovering") return "Stop response was lost";
  if (v.phase === "reconciling") return "Same intent checks the stop marker";
  if (v.phase === "draining") return v.oldAllocationsTerminal ? "Pinned allocations are terminal" : "Waiting for pinned allocations to terminate";
  if (v.phase === "complete") return "Stop and drain receipt complete";
  return "Unknown state";
};
export const group = (v: any) => {
  if (v.phase === "complete") return "done";
  if (["recovering", "reconciling"].includes(v.phase)) return "recovery";
  if (["awaiting-response", "draining"].includes(v.phase)) return "stopped";
  return "planned";
};
const labels: Record<string, string> = {
  ObserveHealthyTarget: "read back every target allocation as healthy",
  ProveMixedRoute: "read back the admitted mixed route generation",
  ReserveStop: "reserve one operation under the exact intent",
  AcceptStopCAS: "Nomad accepts one old-revision CAS stop",
  ReceiveStopResponse: "the stop response includes its revision receipt",
  LoseStopResponse: "the stop commits but its response is lost",
  ResumeSameIntent: "restart the same durable operation",
  RecoverByMarker: "read the exact marker without another stop write",
  AllocationsBecomeTerminal: "Nomad confirms each pinned old allocation is terminal",
  Complete: "record verified stop and drain evidence",
};
export const step = (action: string) => labels[action] ?? null;
