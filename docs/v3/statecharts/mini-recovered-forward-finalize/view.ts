export const title = "Mini recovered-forward finalization and recovery (offline model)";

export const state = (v: any) => {
  const labels: Record<string, string> = {
    "preservation-pending": "A4 maintenance API preserves workloads; host agent fenced",
    "candidate-evidence-held": "Candidate evidence incomplete; host agent remains fenced",
    "finalization-held": "Signed lineage unproven; database write fence remains held",
    "finalize-requested": "Exact finalization request bound to preservation receipt",
    "mutation-fence-released": "Mini accepted finalization; database fence released",
    "normal-activation-pending": "Maintenance API stopped; normal API restart pending",
    "normal-api-active-pending-agent": "Normal API started; host agent remains fenced",
    "normal-api-verified": "Normal API health and exact source verified",
    "normal-api-held": "Normal API health failed; host agent stays fenced",
    "host-agent-restart-pending": "Host-agent restart intent durable",
    "host-agent-active": "Candidate host agent active; receipt pending",
    done: "API and host agent verified; terminal receipt complete",
    held: "Interrupted; same transition journal preserved",
  };
  return labels[v.phase] ?? String(v.phase);
};

export const step = (action: string, from: any, to: any) => {
  // Keep one representative crash after the Mini releases its DB fence and
  // one same-transition recovery edge. TLC still explores every interruption.
  if (action === "CrashOrUncertainEffect" && !["preservation-pending", "mutation-fence-released"].includes(from.phase)) return null;
  if (action === "ResumeSameTransition" && !["preservation-pending", "mutation-fence-released"].includes(to.phase)) return null;
  const labels: Record<string, string> = {
    HoldUnqualifiedCandidate: "hold while candidate source or schema evidence is incomplete",
    HoldUnprovenLineage: "refuse finalization; keep maintenance API and write fence",
    RequestFinalize: "submit exact preservation receipt after signed ancestry verifies",
    MiniAcceptsFinalization: "Mini confirms finalization and releases its write fence",
    StopMaintenanceAPI: "stop maintenance API for normal activation",
    StartNormalAPI: "start the exact candidate in normal mode",
    HoldOnWrongCandidate: "keep the host agent fenced when source or schema mismatches",
    VerifyNormalAPI: "verify authenticated API health, source, and schema",
    HoldOnNormalHealthFailure: "hold host-agent restart when API health fails",
    PersistHostAgentRestartIntent: "persist host-agent restart intent before replacing fence",
    RestartHostAgent: "restart the exact signed candidate host agent",
    WriteTerminalReceipt: "record terminal receipt after API and host-agent checks",
    CrashOrUncertainEffect: "process stops or effect result is uncertain",
    ResumeSameTransition: "reconcile the same journaled transition",
  };
  return labels[action] ?? null;
};

export const group = (v: any) => {
  if (["candidate-evidence-held", "finalization-held", "normal-api-held", "held"].includes(v.phase)) return "held";
  if (["normal-api-active-pending-agent", "normal-api-verified", "host-agent-restart-pending", "host-agent-active", "done"].includes(v.phase)) return "verify";
  if (["mutation-fence-released", "normal-activation-pending"].includes(v.phase)) return "activate";
  return "maintenance";
};

export const colors = { maintenance: "blue", activate: "teal", verify: "purple", held: "orange" };
