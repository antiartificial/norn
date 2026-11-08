export const title = "Nine-node staging topology create and same-intent recovery";
export const colors = { inputs: "blue", apply: "teal", hold: "orange", configure: "purple", done: "tan" };
export const state = (v: any) => {
  if (v.phase === "inputs") return "Validate fresh plan and bootstrap inputs";
  if (v.phase === "secrets") return "Prepare protected owner bootstrap secret files";
  if (v.phase === "ready") return "Validated inputs and secrets; ready for exact apply";
  if (v.phase === "applying") {
    if (v.providerNodes === 0) return "Ready to create; no provider nodes exist";
    if (v.providerNodes === 9) return "Nine provider nodes created; verify state";
    return "Partial provider create (1–8 of 9 nodes)";
  }
  if (v.phase === "checking") return "Read back full provider state";
  if (v.phase === "held") {
    if (v.providerMismatch) return "Hold: state or provider identity mismatch";
    if (v.freshReadback) return "Hold: exact state read back; same-intent recovery available";
    return "Hold: read back provider state before recovery";
  }
  if (v.phase === "recovering") return "Resume same intent after exact readback";
  if (v.phase === "created") return "Exact nine-node state confirmed";
  if (v.phase === "inventory") return "Generate exact inventory";
  if (v.phase === "pki") return "Prepare cluster-scoped PKI";
  if (v.phase === "configuring") {
    if (v.configuredNodes === 0) return "Begin node configuration";
    if (v.configuredNodes === 9) return "All nodes configured; ready to seal";
    return "Partial node configuration (1–8 of 9 nodes)";
  }
  if (v.phase === "complete") return "Ready receipt records verified Fleet";
  return "Unknown state";
};
export const group = (v: any) => {
  if (["inputs", "secrets", "ready"].includes(v.phase)) return "inputs";
  if (v.phase === "held") return "hold";
  if (["applying", "checking", "recovering", "created"].includes(v.phase)) return "apply";
  if (["inventory", "pki", "configuring"].includes(v.phase)) return "configure";
  if (v.phase === "complete") return "done";
  return "inputs";
};
export const step = (action: string) => ({
  ValidateAllInputs: "validate plan, account, backend, SSH, scope, and bootstrap inputs",
  MaterializeBoundSecrets: "materialize owner-only protected bootstrap files",
  RejectInvalidOrStaleInput: "hold on absent, invalid, or stale input",
  AdmitOneUseIntent: "bind immutable intent and consume one-use nonce",
  CreateOneNode: "create one node and record its response",
  ProviderEffectThenLostResponse: "provider creates a node but response is lost",
  ControllerCrash: "controller stops with durable intent",
  BeginStateCheck: "start exact state readback after apply",
  ReadBackProviderState: "read provider IDs and backend state before recovery",
  ConfirmSuccessfulApplyState: "confirm all nine nodes and matching identities",
  HoldMismatchedState: "hold when provider and state disagree",
  DetectUnclassifiedResource: "provider inventory reveals an unclassified resource",
  RecoverSameIntent: "resume the same intent after exact readback",
  GenerateInventory: "derive inventory only from all nine nodes",
  PrepareClusterPKI: "generate cluster-scoped node identities",
  ConfigureOneNode: "configure one node from verified inventory",
  CompleteWithReceipt: "record terminal readiness receipt",
}[action.split("(")[0]] ?? null);
