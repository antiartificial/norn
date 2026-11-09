export const title = "Online Norn upgrade with fenced recovery";

export const state = (v) => {
  if (v.phase === "held") return v.requestedTarget === "other" ? "Held on changed intent" : "Held for same-intent recovery";
  if (v.phase === "done") return "Target release verified";
  if (v.phase === "plan") return "Old release serving";
  if (v.phase === "prepared") return "Backup and shadow ready";
  if (v.phase === "expanded") return "Intent bound; old release serving";
  if (v.phase === "candidate-starting") return "Expanded schema; old release serving";
  if (v.phase === "candidate-ready") return "Passive candidate started";
  if (v.phase === "candidate-verified") return "Candidate verified; old release serving";
  if (v.phase === "target-serving") return v.targetHasWritten ? "Target owns writes; old process retained" : "Target owns traffic and writes";
  if (v.phase === "old-retired") return "Old writer retired";
  if (v.phase === "contracted") return "Old schema contract retired";
  return v.phase;
};

export const step = (action, from, to) => {
  if (action === "ControllerCrashOrAmbiguousEffect" &&
      !["expanded", "target-serving", "old-retired", "contracted"].includes(from.phase)) return null;
  if (action === "ResumeSameIntent" &&
      !["candidate-starting", "target-serving", "contracted"].includes(to.phase)) return null;
  return ({
  PrepareBackupAndShadow: "controller validates backup and candidate shadow",
  BindIntent: "controller binds the signed release intent",
  ExpandSchemaOnline: "controller expands schema while old release serves",
  StartPassiveCandidate: "controller starts target in passive mode",
  VerifyCandidate: "candidate passes compatibility and readiness checks",
  CandidateFailsBeforeCutover: "candidate readiness fails; old release keeps serving",
  AtomicWriterAndTrafficHandoff: "controller atomically transfers traffic and writer authority",
  RecordTargetWrite: "target release accepts a write",
  VerifyCutover: "controller verifies target-only serving state",
  RetireOldWriter: "controller retires the old process after cutover proof",
  ContractSchema: "controller contracts schema after old writer retirement",
  WriteTerminalReceipt: "controller records the verified terminal receipt",
  ControllerCrashOrAmbiguousEffect: "controller stops with an uncertain effect",
  ResumeSameIntent: "successor reconciles the same bound intent",
  ChangedIntentStaysHeld: "new target conflicts with the bound intent",
  RetryBoundIntent: "operator retries the journaled target",
  HeldWait: "controller waits for recovery evidence",
  DoneWait: "upgrade receipt remains terminal",
  }[action] ?? action);
};

export const group = (v) => v.phase === "held" ? "Held" : v.phase === "done" ? "Complete" : "Transition";
export const colors = { Held: "orange", Complete: "teal", Transition: "blue" };
