# FreshFleetNornAuthority

TLC reached 19 states and 21 steps. The chart draws 24 states and 22 arrows.
The chart does not draw 2 steps that stay in one chart state.
The step create eight nodes, lose response changes Norn and Provider at the same time.
The step create ninth node changes Norn and Provider at the same time.
The step register target and transfer authority fence changes Norn and Target at the same time.

## Rules that hold
TypeOK, EffectNeedsConsumedGrant, ConsumedGrantWasIssued, GrantTracksExactRemainder, AttemptNeedsProtectedDispatch, AdmissionBelongsToCurrentGrant, RecoveryKeepsIntent, RegistrationNeedsTerminalReceipt, NoTargetDuringCreate, NoDeadlock
