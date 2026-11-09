export const title = "Fresh DO Fleet create and recovery (bounded contract)";
export const colors = { inputs: "blue", create: "teal", held: "orange", done: "purple" };
export const state = (v: any) => {
  if (v.phase === "inputs") return "Gather and validate protected inputs";
  if (v.phase === "ready-to-apply") return "Exact one-use apply admitted";
  if (v.phase === "creating") return "Provider create in progress (partial state possible)";
  if (v.phase === "created") return "Fresh provider state reconciled";
  if (v.phase === "configuring") return "Generate PKI and configure created nodes";
  if (v.phase === "configured") return "All node groups configured";
  if (v.phase === "held") return "Hold: require readback or explicit cleanup";
  if (v.phase === "recovering") return "Resume exact intent after readback";
  if (v.phase === "cleanup") return "Exact IDs read back; cleanup approved";
  if (v.phase === "complete") return "Fresh Fleet ready with terminal receipt";
  if (v.phase === "retired") return "Exact partial resources retired";
  return "Unknown state";
};
export const group = (v: any) => {
  if (v.phase === "held" || v.phase === "recovering" || v.phase === "cleanup") return "held";
  if (v.phase === "complete" || v.phase === "retired") return "done";
  if (["creating", "created", "configuring", "configured"].includes(v.phase)) return "create";
  return "inputs";
};
export const step = (action: string, from: any, to: any) => {
  if (action.startsWith("CreateGroup") || action.startsWith("ConfigureGroup")) return null;
  return ({
    SupplyReviewedPlan: "bind protected plan digest",
    BindCapacityIntent: "bind capacity UUID and one-use nonce",
    ProviderAccountPreflight: "validate account and exact state backend",
    PrepareSSH: "pin runner key and admin ingress",
    AdmitIntent: "admit one exact apply intent",
    RejectOrExpire: "reject stale, incomplete, or expired input",
    CreateGroup: "create one provider resource group",
    LoseCreateResponse: "provider effect may exist; response is lost",
    ReadProviderState: "read back exact provider state and identities",
    PreparePKI: "generate cluster-scoped PKI after create",
    ConfigureGroup: "configure one group through Ansible",
    ApproveExactCleanup: "approve exact partial-create retirement",
    AuthorizeCleanup: "require fresh readback before cleanup",
    RetireExactResources: "retire only reconciled resource IDs",
    Complete: "record ready receipt",
    ControllerCrash: "controller stops; durable intent remains",
    RecoverSameIntent: "recover same intent after exact state proof",
    ResumeAfterReadback: "resume at observed create/configure checkpoint",
  } as Record<string, string>)[action.split("(")[0]] ?? null;
};
