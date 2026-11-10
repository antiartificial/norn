export const title = "Mini queued-upgrade host-agent stability gate (offline model)";

export const state = (v: any) => {
  const labels: Record<string, string> = {
    checking: `Checking LaunchAgent stability (${v.stableSamples}/3 stable samples)`,
    admitted: "Exact host agent stable; upgrade gate admitted",
    held: "Stability window exhausted; source left untouched",
    promoted: "Promotion may proceed after stable proof",
  };
  return labels[v.phase] ?? String(v.phase);
};

export const step = (action: string) => {
  const labels: Record<string, string> = {
    ObserveCurrent: "sample the same LaunchAgent process",
    ObserveMissingAgent: "agent is absent; reset the stability window",
    LaunchdRestarts: "Launchd replaces the process between samples",
    Promote: "allow the upgrade path after three stable samples",
  };
  return labels[action] ?? null;
};

export const group = (v: any) => v.phase === "held" ? "held" : v.phase === "promoted" ? "done" : "checking";
export const colors = { checking: "blue", held: "orange", done: "purple" };
