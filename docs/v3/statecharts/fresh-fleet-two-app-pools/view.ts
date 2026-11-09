export const title = "Fresh Fleet create and recovery with two app pools";

const labels: Record<string, string> = {
  inputs: "Checking Fleet inputs",
  ready: "Inputs ready",
  applying: "Creating Fleet pools",
  held: "Held for exact readback",
  recovering: "Resuming the same intent",
  inventory: "Building complete inventory",
  configuring: "Configuring both app pools",
  complete: "Terminal receipt verified",
};

export const state = (v: Record<string, any>) => {
  if (v.phase === "held") {
    if (!v.intentBound) return "Held: invalid inputs, no provider effects";
    if (v.providerMismatch) return "Held: provider identity mismatch";
    if (v.uncertainA || v.uncertainB) return "Held: a pool create response is unknown";
    return "Held: readback complete, same intent can resume";
  }
  if (v.phase === "applying" || v.phase === "recovering") {
    const confirmed = Number(v.observedA) + Number(v.observedB);
    if (confirmed === 2) return labels[v.phase] + ": both pools confirmed";
    if (confirmed === 1) return labels[v.phase] + ": one pool confirmed";
  }
  if (v.phase === "configuring") {
    const configured = Number(v.configuredA) + Number(v.configuredB);
    if (configured === 2 && v.workloads.statefulRecoveryVerified && v.workloads.generalReady) {
      return v.workloads.statefulReady ? "General and stateful workloads ready" : "Verifying stateful readiness";
    }
    if (configured === 2 && v.workloads.generalPlaced && v.workloads.statefulPlaced) {
      return "Workloads placed; readiness pending";
    }
    if (configured === 2) return "Both pools configured; placement pending";
    if (configured === 1) return "One pool configured";
  }
  return labels[v.phase] || v.phase;
};

export const step = (action: string) => {
  const labels: Record<string, string | null> = {
    ValidateInputs: "inputs pass validation",
    RejectInputs: "input validation holds before effects",
    BindIntent: "bind one intent and dispatch nonce",
    CreateA: "create a declared app pool",
    LoseCreateAResponse: "a pool create response is lost",
    CreateB: "create a declared app pool",
    LoseCreateBResponse: "a pool create response is lost",
    ReadbackA: "read back a pool by exact identity",
    ReadbackB: "read back a pool by exact identity",
    DetectProviderMismatch: "provider readback does not match",
    ResumeSameIntent: "resume the same intent",
    BuildInventory: "confirm both pools and build inventory",
    GeneratePKI: null,
    MaterializeSecrets: null,
    ConfigureFleet: "begin host configuration",
    ConfigureA: "configure one app pool",
    ConfigureB: "configure one app pool",
    PlaceGeneralWorkload: "place workload on its declared role pool",
    PlaceStatefulWorkload: "place workload on its declared role pool",
    VerifyGeneralReadiness: "prove workload readiness for its role",
    VerifyStatefulRecovery: "prove workload readiness for its role",
    CommitReceipt: "commit terminal Fleet receipt",
    Done: null,
  };
  return labels[action] ?? null;
};

export const group = (v: Record<string, any>) => {
  if (v.phase === "held") return "held";
  if (v.phase === "complete") return "complete";
  if (v.phase === "configuring") return "configuring";
  return "normal";
};

export const colors = {
  normal: "blue",
  held: "orange",
  configuring: "purple",
  complete: "teal",
};
