export const title = "Mini direct V3 upgrade and recovery (offline model)";

export const state = (v: any) => {
  if (v.phase === "held" && v.intentTarget === "other") return "Changed target rejected; transition held";
  const labels: Record<string, string> = {
    plan: "Plan exact source and target",
    backup: "Protected backup and target shadow",
    prepared: "Writer set proven; intent ready",
    "host-agent-fencing": "Fence host agent",
    "api-fencing": "Fence old API",
    "legacy-fenced": "Old writers stopped; writes fenced",
    "migration-started": "Apply schema migration",
    "schema-migrated": "Schema 51 ready; target pinned",
    "candidate-started": "Start signed target",
    "candidate-healthy": "Target healthy; preservation checked",
    done: "Transition receipt complete",
    held: "Interrupted; journal preserved",
  };
  return labels[v.phase] ?? String(v.phase);
};

export const step = (action: string, from: any, to: any) => {
  // Keep one post-migration crash/recovery example. Other crash and resume
  // branches remain checked by TLC but add repeated arrows to the chart.
  if (action === "CrashOrAmbiguousOutcome" && from.phase !== "schema-migrated") return null;
  if (action === "ResumeSameIntent" && to.phase !== "schema-migrated") return null;
  const labels: Record<string, string> = {
    ProveWriters: "prove all writers compatible or fenceable before transition",
    HoldUnknownWriters: "unknown writer holds after safe backup and shadow preparation",
    RefreshWriterProof: "collect fresh writer proof",
    TakeBackupAndShadow: "verify protected backup and exact candidate shadow",
    BindJournal: "bind exact signed target in the generation journal",
    FenceHostAgent: "stop and fence host agent",
    FenceAPI: "drain and fence old API",
    BeginMigration: "persist migration start",
    FinishMigration: "complete resumable schema migration",
    StartCandidate: "start exact signed target and matching host agent",
    VerifyCandidateAndPreservation: "check health and workload preservation",
    Finalize: "write terminal receipt and release mutation fence",
    CrashOrAmbiguousOutcome: "process stops or effect outcome is uncertain",
    ResumeSameIntent: "reconcile and resume the same signed target",
    TryDifferentTarget: "changed target is held against the journal",
  };
  return labels[action] ?? null;
};

export const group = (v: any) => {
  if (v.phase === "held") return "held";
  if (["candidate-started", "candidate-healthy", "done"].includes(v.phase)) return "verify";
  if (["host-agent-fencing", "api-fencing", "legacy-fenced", "migration-started", "schema-migrated"].includes(v.phase)) return "transition";
  return "prepare";
};

export const colors = { prepare: "blue", transition: "teal", verify: "purple", held: "orange" };
