export const title = "Norn Fleet release and recovery (offline model)";
export const state = (v) => {
  if (v.phase === "complete") return "Target healthy, pins durable";
  if (v.phase === "held") return v.unknownObserved ? "Unknown bytes observed, held" : "Failure held";
  if (v.phase === "intent-ready") return "Signed intent ready";
  if (v.phase === "staging") return "Staging and verifying target bytes";
  if (v.phase === "staged") return "Target staged on all controls";
  if (v.phase === "stopping") return "Stopping old services";
  if (v.phase === "stopped") return "All old services stopped";
  if (v.phase === "promoted") return "Target selected on all controls";
  if (v.phase === "starting") return "Starting target services";
  if (v.phase === "verify") return "Checking singleton health";
  if (v.phase === "healthy") return "Target healthy, pins pending";
  return v.phase;
};
export const group = (v) => v.phase === "held" ? "held" :
  ["complete", "healthy", "verify", "starting"].includes(v.phase) ? "recovery" : "release";
export const colors = { release: "blue", recovery: "teal", held: "orange" };
export const step = (action, from, to) => {
  if (action === "FailClosed" && !["intent-ready", "stopping", "starting"].includes(from.phase)) return null;
  if (action === "ObserveThird" && !["intent-ready", "stopped", "starting", "verify"].includes(from.phase)) return null;
  if (action === "ResumeSameIntent" && to.phase !== "starting") return null;
  return ({
  Admit: "controller admits the fresh signed intent",
  Stage: from.phase === to.phase ? null : "all target bytes verify on every control",
  BeginStop: "all three staged, begin the stop window",
  Stop: from.phase === to.phase ? null : "all old services stop",
  Promote: from.phase === to.phase ? null : "select target bytes on every stopped control",
  BeginStart: "all three select target, begin startup",
  Start: from.phase === to.phase ? null : "target services start on all controls",
  VerifyHealthy: "singleton and backend health pass",
  PersistPins: "persist target pins and retire runner",
  ResumeSameIntent: "resume same signed target from durable checkpoint",
  ObserveThird: "unexpected bytes found, hold without overwrite",
  FailClosed: from.intentValid ? "failure or ambiguous outcome, preserve journal" : "unsigned, expired or mismatched intent is rejected",
  Done: null,
}[action] ?? null);
};
