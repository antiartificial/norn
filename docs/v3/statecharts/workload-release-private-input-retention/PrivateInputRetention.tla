---------------- MODULE PrivateInputRetention ----------------
EXTENDS Naturals, FiniteSets, TLC

CONSTANT NoOwner
Phases == {"Pending", "Held", "Complete"}
Owners == {NoOwner, "Worker", "Successor"}

VARIABLES phase, input, tombstone, owner
vars == <<phase, input, tombstone, owner>>

Init ==
  /\ phase = "Pending"
  /\ input = TRUE
  /\ tombstone = FALSE
  /\ owner = "Worker"

Hold ==
  /\ phase = "Pending"
  /\ owner # NoOwner
  /\ phase' = "Held"
  /\ UNCHANGED <<input, tombstone, owner>>

Recover ==
  /\ phase = "Held"
  /\ owner # NoOwner
  /\ phase' = "Pending"
  /\ UNCHANGED <<input, tombstone, owner>>

CompleteRelease ==
  /\ phase = "Pending"
  /\ owner # NoOwner
  /\ phase' = "Complete"
  /\ UNCHANGED <<input, tombstone, owner>>

DeleteInputs ==
  /\ phase = "Complete"
  /\ owner # NoOwner
  /\ input = TRUE
  /\ input' = FALSE
  /\ tombstone' = TRUE
  /\ UNCHANGED <<phase, owner>>

LoseWorker ==
  /\ owner # NoOwner
  /\ owner' = NoOwner
  /\ UNCHANGED <<phase, input, tombstone>>

SuccessorClaims ==
  /\ owner = NoOwner
  /\ owner' = "Successor"
  /\ UNCHANGED <<phase, input, tombstone>>

RetryDeletion ==
  /\ phase = "Complete"
  /\ owner # NoOwner
  /\ input = FALSE
  /\ tombstone = TRUE
  /\ UNCHANGED vars

Done == phase = "Complete" /\ input = FALSE /\ tombstone = TRUE /\ UNCHANGED vars

Next == Hold \/ Recover \/ CompleteRelease \/ DeleteInputs \/ LoseWorker \/ SuccessorClaims \/ RetryDeletion \/ Done

InputRetainedUntilComplete == phase # "Complete" => input
DeletionHasReceipt == ~input => tombstone /\ phase = "Complete"
TombstoneRequiresAbsentInput == tombstone => ~input

Spec == Init /\ [][Next]_vars

THEOREM Spec => []InputRetainedUntilComplete
THEOREM Spec => []DeletionHasReceipt
THEOREM Spec => []TombstoneRequiresAbsentInput
============================================================
