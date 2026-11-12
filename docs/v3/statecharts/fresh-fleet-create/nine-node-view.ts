export const title = "Fresh Fleet nine-node create and recovery";
export const colors = { inputs: "blue", apply: "teal", held: "orange", bootstrap: "purple", done: "yellow" };

const inputState = (v: any) => {
  const plan = v.planReviewed ? "reviewed" : "pending";
  const capacity = v.capacityBound && v.backendBound ? "bound" : "pending";
  const provider = v.accountScoped && v.sshPinned ? "attested" : "pending";
  return `Inputs — plan ${plan}; capacity/state ${capacity}; provider/SSH ${provider}`;
};

export const state = (v: any) => {
  if (v.phase === "inputs") return inputState(v);
  if (v.phase === "ready") return "One reviewed nine-node apply intent is bound";
  if (v.phase === "applying") return "One-use apply running against reviewed nine-node plan";
  if (v.phase === "held" && !v.controllerRunning) return "Apply runner retired; controller restart required";
  if (v.phase === "held" && !(v.providerReadback || []).length) return "Controller restarted; exact provider/state readback required";
  if (v.phase === "held" && (v.providerReadback || []).length !== (v.stateReadback || []).length) return "Provider/state readback differs; hold for reconciliation";
  if (v.phase === "held") return "Eight-node readback matches; same-intent recovery may resume";
  if (v.phase === "recovering") return "Create only the exact missing node under same intent";
  if (v.phase === "created") return "Nine provider IDs match nine state entries";
  if (v.phase === "configuring") return "Configure all nine nodes from exact inventory";
  if (v.phase === "finalizing") return "Retire one-use runner before terminal receipt";
  if (v.phase === "complete") return "Nine nodes configured; terminal receipt written";
  return "Unknown state";
};

export const group = (v: any) => {
  if (v.phase === "inputs") return "inputs";
  if (v.phase === "held" || v.phase === "recovering") return "held";
  if (["configuring", "created", "finalizing"].includes(v.phase)) return "bootstrap";
  if (v.phase === "complete") return "done";
  return "apply";
};

export const step = (action: string) => ({
  ReviewPlan: "review the exact plan and digest",
  BindCapacityAndState: "bind capacity and backend lineage",
  AttestProviderAndSSH: "attest provider account and pin SSH identity",
  BindIntent: "bind one intent to the reviewed plan and state",
  AdmitApply: "consume one-use apply authorization",
  CreateFirstEight: "create a node and record its provider/state identities",
  LoseApplyResponse: "lose apply response after partial create; retire runner",
  LoseStateWriteResponse: "lose apply response after partial create; retire runner",
  RestartController: "restart controller with durable intent and failure receipt",
  ReadBackPartial: "read exact provider and state identities",
  AdmitSameIntentRecovery: "bind fresh recovery authorization to same intent and remainder",
  CreateNinthFromBoundRemainder: "create only the approved missing node",
  ReadBackComplete: "verify all nine identities before bootstrap",
  PreparePKIAndBootstrap: "prepare PKI after exact readback",
  ConfigureNextNode: "configure the next node from exact inventory",
  RetireRecoveryRunner: "retire one-use runner after bootstrap",
  WriteTerminalReceipt: "write terminal receipt after runner retirement",
}[action] ?? null);
