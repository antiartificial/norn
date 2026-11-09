export const title = "Two simultaneous release requests with one stable key";

export const state = (v: any) => {
  if (v.result[1] === "fresh" && v.result[2] === "replay") return "One acceptance; second caller gets replay";
  if (v.result[2] === "fresh" && v.result[1] === "replay") return "One acceptance; first caller gets replay";
  if (v.result[1] === "fresh" || v.result[2] === "fresh") return "One caller accepted; other still resolving";
  if (v.result[1] === "replay" || v.result[2] === "replay") return "Stored acceptance replayed";
  if (v.phase[1] === "lookup" && v.phase[2] === "lookup") return "Both handlers begin replay lookup";
  if (v.phase[1] === "derive" || v.phase[2] === "derive") return "Replay missed; handlers read current evidence";
  if (v.phase[1] === "accept" || v.phase[2] === "accept") return "Both requests reach atomic acceptance";
  return "Requests complete";
};

export const step = (action: string, from: any, to: any) => {
  if (action === "LookupReplayMiss") return "initial replay lookup finds no accepted request";
  if (action === "ReadMutableEvidence") return "handler reads qualification, policy, route, and source";
  if (action === "CommitFreshAcceptance") return "first atomic acceptance stores the operation";
  if (action === "ResolveAcceptanceRace") return "store returns the winner's stored acceptance";
  if (action === "LookupReplayHit") return "later replay lookup returns the stored operation";
  if (action === "Done") return null;
  return action;
};

export const group = (v: any) =>
  v.freshAcceptances === 1 ? "accepted" : "admitting";

export const colors = { admitting: "blue", accepted: "teal" };
