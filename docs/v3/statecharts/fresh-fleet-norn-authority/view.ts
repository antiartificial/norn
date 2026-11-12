export const title = "Fresh Fleet create authority before target registration";
export const state = (v) => ({
  Norn: v.phase,
  Provider: v.providerNodes === 0 ? "empty" : `${v.providerNodes} nodes observed`,
  Target: v.targetRegistered ? "registered" : "not registered",
});
export const step = (action) => ({
  BindIntent: "bind exact reviewed create intent",
  BindProtectedDispatch: "bind the exact protected runner dispatch",
  StartInitialAttempt: "start pre-registration attempt",
  RecordInitialAdmission: "verify empty state and admit the initial remainder",
  ConsumeInitialGrant: "consume exact one-use create grant",
  LoseInitialConsumeResponse: "lose grant-consume response",
  ReadBackConsumedInitialGrant: "read consumed-grant status without replay",
  CreateEightLoseResponse: "create eight nodes, lose response",
  ReadBackExactRemainder: "read exact provider and state remainder",
  RecordExactRecoveryReadback: "record exact verifier recovery readback",
  StartSameIntentRecovery: "restart under same intent",
  RecordRecoveryAdmission: "verify provider and state readback for this attempt",
  ConsumeRemainderGrant: "consume grant for exact one-node remainder",
  CreateNinth: "create ninth node",
  ReadBackAllNodes: "verify all nine provider and state identities",
  RecordCompleteTerminalReceipt: "record complete terminal receipt",
  RegisterTargetAndTransferFence: "register target and transfer authority fence",
  HoldUnknownReadback: "hold on mismatched readback",
  Done: null,
}[action] ?? action);
