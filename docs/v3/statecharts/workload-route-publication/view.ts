export const title = "Workload route publication and recovery";
export const colors = { active: "blue", publish: "teal", recovery: "orange", proof: "purple", held: "tan", done: "pink" };
export const state = (v: any) => {
  if (v.phase === "warming") return "Target is healthy, old route serves";
  if (v.phase === "publishing") return `Publishing the same route step to ${v.published.length} of 2 ingress nodes`;
  if (v.phase === "recovering") return "Publisher response was lost; intent is retained";
  if (v.phase === "reconciling") return "Route and receipt committed; same intent refreshes route proof";
  if (v.phase === "committing") return "Independent route and traffic proof passed";
  if (v.phase === "proving-traffic") return "Every node serves the route; traffic proof is pending";
  if (v.phase === "routed") return "Active route and mixed receipt committed atomically";
  if (v.phase === "stop-ready") return "Fresh route, inventory, and target proof authorize old stop";
  if (v.phase === "stop-authorized") return "Old-generation stop is authorized";
  if (v.phase === "held") return "Authority, inventory, or health changed; release is held";
  return "Unknown state";
};
export const group = (v: any) => {
  if (v.phase === "stop-ready") return "proof";
  if (v.phase === "routed") return "done";
  if (v.phase === "stop-authorized") return "done";
  if (v.phase === "held") return "held";
  if (v.phase === "recovering") return "recovery";
  if (["proving-traffic", "committing", "reconciling"].includes(v.phase)) return "proof";
  if (v.phase === "publishing") return "publish";
  return "active";
};
const labels: Record<string, string> = {
  ReserveMixedStep: "reserve generation 11 and the admitted mixed step",
  PublishOneNode: "publish the same opaque intent to one current ingress node",
  LosePartialResponse: "the first node commits but its response is lost",
  InventoryDriftDuringRoute: "the live ingress inventory changes during publication",
  InventoryDriftAfterReceipt: "inventory changes after route receipt; old stop stays held",
  CompetingRouteChangeAfterReceipt: "another route writer advances after receipt; old stop stays held",
  ResumeSameIntent: "reacquire authority and reconcile the same generation",
  ReadEveryNode: "independently read back the exact route on every node",
  ReconfirmEveryNode: "refresh exact route readback after same-intent recovery",
  ProveTraffic: "confirm application and public-path traffic",
  AdvanceActiveRoute: "atomically compare-and-swap the route and commit its receipt",
  CompetingRouteChange: "detect another route writer and hold the old stop",
  LoseActiveCASResponse: "active route and receipt commit; controller loses the response",
  ResumeBeforeRouteCAS: "reacquire authority and resume the same route generation",
  ResumeAfterRouteCAS: "reacquire authority and reconcile the committed generation",
  ControllerCrashBeforeReceipt: null,
  LostAuthority: "operation claim or app lock is lost",
  AuthorizeOldStop: "recheck current inventory, route, and target before the old stop",
  RevalidateBeforeOldStop: "freshly confirm route, inventory, and target before old stop",
  InventoryDriftAfterReceipt: "inventory changes after route receipt; old stop stays held",
  TargetHealthLost: "target health regresses during rollout",
  ControllerCrash: "the route controller stops",
  HoldOnLostAuthority: "claim or app lock is lost",
};
export const step = (action: string) => labels[action] ?? null;
