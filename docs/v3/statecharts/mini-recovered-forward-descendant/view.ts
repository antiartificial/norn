export const title = "Mini recovered-forward descendant upgrade gate";

export const state = (v: any) => {
  const labels: Record<string, string> = {
    "recovery-ready": "Recovery receipt verified; recovery release active",
    "descendant-running": "Signed descendant active; recovery receipt retained",
    "unrelated-running": "Signed but unrelated release active",
    "upgrade-authorized": v.activeRelease === "descendant"
      ? "Later upgrade allowed from signed descendant"
      : "First forward upgrade allowed",
    held: "Upgrade held by operation-mode or recovery-lineage gate",
  };
  return labels[v.phase] ?? String(v.phase);
};

export const step = (action: string, from: any, to: any) => {
  const labels: Record<string, string> = {
    CheckUpgradeGate: to.phase === "held"
      ? (!from.modeAccepted && from.activeRelease === "descendant"
          ? "request upgrade while the operation mode is not admitted"
          : "check retained evidence and current signed lineage; upgrade held")
      : "check retained evidence and current signed lineage",
    PromoteFirstSignedDescendant: "promote the first signed descendant through the controller",
    RunUnrelatedSignedRelease: "observe a signed release outside the recovery candidate's lineage",
  };
  return labels[action] ?? null;
};

export const group = (v: any) => {
  if (v.phase === "held" || v.phase === "unrelated-running") return "held";
  if (v.phase === "upgrade-authorized") return "ready";
  if (v.phase === "descendant-running") return "forward";
  return "recovery";
};

export const colors = { recovery: "blue", forward: "teal", ready: "purple", held: "orange" };
