export const title = "Existing Fleet Genesis and fresh-Fleet boundary";
export const colors = { install: "blue", rollout: "teal", held: "orange", absent: "tan" };

export const state = (v) => {
  if (v.phase === "start") return "Inputs not checked";
  if (v.phase === "inputs-checked") return "Protected inputs checked";
  if (v.phase === "dormant-installed") return "Controller installed, services stopped";
  if (v.phase === "genesis-ready") return "Signed Genesis intent journaled";
  if (v.phase === "enabled") return "Genesis explicitly enabled";
  if (v.phase === "running") {
    if (v.staged.length < 3) return "Staging target on all controls";
    if (v.promoted.length === 0) return "All controls staged";
    if (v.promoted.length === 1) return "First passive control promoted";
    if (v.promoted.length === 2) return "Both passive controls promoted";
    return "Serving control promoted";
  }
  if (v.phase === "held" && v.attempt === "interrupted" && v.fenceEpoch < 2) {
    return v.recoveryProof ? "One-use recovery proof accepted" : "Held for one-use recovery proof";
  }
  if (v.phase === "held" && v.attempt === "interrupted") return "Held after the modeled recovery retry";
  if (v.phase === "held") return `Held (${v.heldReason})`;
  if (v.phase === "complete") return "Genesis complete";
  if (v.phase === "fresh-fleet-blocked") return "Fresh Fleet request blocked, no provider effects";
  return String(v.phase);
};

export const group = (v) => {
  if (["start", "inputs-checked", "dormant-installed", "genesis-ready", "enabled"].includes(v.phase)) return "install";
  if (["running", "complete"].includes(v.phase)) return "rollout";
  if (v.phase === "held") return "held";
  return "absent";
};

export const step = (action, from, to) => {
  if (action === "ControllerCrash") {
    return from.promoted.length === 0 && from.fenceEpoch <= 2
      ? "the controller stops during a member effect"
      : null;
  }
  if (action === "ResumeSameIntent") {
    return to.phase === "running" && to.promoted.length === 0 && to.staged.length < 3
      ? "resume the same intent once after independent recovery proof"
      : null;
  }
  return ({
  ReviewValidInputs: "owner-protected inputs pass offline checks",
  RejectInvalidInputs: "an input mismatch blocks the install",
  InstallDormantController: "install the controller with services stopped",
  VerifyGenesisIntent: "verify and journal the signed Genesis intent",
  EnableGenesis: "explicitly enable Genesis",
  StartGenesis: "start the one-use Genesis attempt",
  "StageControl": from.phase === "running" && to.phase === "running"
    ? (from.staged.length < 2 && to.staged.length < 3 ? null : "all three controls finish staging and verification")
    : null,
  "PromotePassive": "promote a passive control after all controls are staged",
  PromoteServing: "promote the serving control last",
  CompleteGenesis: "verify target capability and record completion",
  ControllerCrash: "the controller stops during an effect",
  AuthorizeRecovery: "an independent recovery proof is recorded",
  ResumeSameIntent: "resume the same intent under a new fence",
  FreshFleetRequestIsBlocked: "request fresh provider creation, but no create lane exists",
  Done: null,
}[action] ?? null);
};
