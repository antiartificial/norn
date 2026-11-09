export const title = "DO Genesis readback: source pin and one-use recovery";

export const state = (v: any) => {
  if (v.phase === "start") return "Readback not prepared";
  if (v.phase === "intent-staged" && v.binding !== "valid") return "Intent has mismatched bindings";
  if (v.phase === "intent-staged") return v.installedSource === "old" ? "Fresh intent, old verifier" : "Fresh intent, merged verifier";
  if (v.phase === "readback-held") return "Readback held on stale source";
  if (v.phase === "verifier-installed") return "Merged verifier installed, timers off";
  if (v.phase === "readback-complete") return "Readback verified";
  return "Intent rejected";
};

export const step = (action: string, from: any, to: any) => {
  if (action === "StageReadbackIntent") return to.binding === "valid" ? "the owner signs a fresh intent for the verified state and host keys" : "an intent has mismatched state, host-key, or signature inputs";
  if (action === "RejectInvalidReadbackInputs") return "the controller rejects invalid readback inputs";
  if (action === "ReadbackWithStaleSource") return "readback finds the installed source pin is stale";
  if (action === "InstallReviewedVerifier") return "the protected installer updates the verifier and leaves timers off";
  if (action === "ReadbackWithCurrentSource") return "a fresh one-use intent passes readback";
  if (action === "Done") return "the operator inspects the terminal receipt";
  return null;
};

export const group = (v: any) => {
  if (v.phase === "readback-complete") return "verified";
  if (v.phase === "readback-held" || v.phase === "intent-rejected") return "held";
  return "pending";
};

export const colors = { pending: "blue", held: "orange", verified: "teal" };
