---------------- MODULE ConcurrentAdmission ----------------
EXTENDS Naturals, TLC

CONSTANT RetryAfterFence

VARIABLES a, b, committed, resultA, resultB
vars == <<a, b, committed, resultA, resultB>>

Init ==
  /\ a = "initial"
  /\ b = "initial"
  /\ committed = FALSE
  /\ resultA = "none"
  /\ resultB = "none"

MissA == a = "initial" /\ a' = "prepared" /\ UNCHANGED <<b, committed, resultA, resultB>>
MissB == b = "initial" /\ b' = "prepared" /\ UNCHANGED <<a, committed, resultA, resultB>>
CommitA == a = "prepared" /\ ~committed /\ committed' = TRUE /\ a' = "accepted" /\ UNCHANGED <<b, resultA, resultB>>
CommitB == b = "prepared" /\ ~committed /\ committed' = TRUE /\ b' = "accepted" /\ UNCHANGED <<a, resultA, resultB>>

FenceLoseA ==
  /\ a = "prepared" /\ committed /\ resultA = "none"
  /\ IF RetryAfterFence THEN a' = "rechecking" ELSE a' = "failed"
  /\ UNCHANGED <<b, committed, resultA, resultB>>
FenceLoseB ==
  /\ b = "prepared" /\ committed /\ resultB = "none"
  /\ IF RetryAfterFence THEN b' = "rechecking" ELSE b' = "failed"
  /\ UNCHANGED <<a, committed, resultA, resultB>>
ReplayA == a = "rechecking" /\ committed /\ a' = "replayed" /\ UNCHANGED <<b, committed, resultA, resultB>>
ReplayB == b = "rechecking" /\ committed /\ b' = "replayed" /\ UNCHANGED <<a, committed, resultA, resultB>>

Finish ==
  /\ (a = "accepted" \/ a = "replayed" \/ a = "failed")
  /\ (b = "accepted" \/ b = "replayed" \/ b = "failed")
  /\ resultA' = IF a = "accepted" THEN "fresh" ELSE IF a = "replayed" THEN "replay" ELSE "error"
  /\ resultB' = IF b = "accepted" THEN "fresh" ELSE IF b = "replayed" THEN "replay" ELSE "error"
  /\ UNCHANGED <<a, b, committed>>

Next == MissA \/ MissB \/ CommitA \/ CommitB \/ FenceLoseA \/ FenceLoseB \/ ReplayA \/ ReplayB \/ Finish
Spec == Init /\ [][Next]_vars

TypeOK ==
  /\ a \in {"initial", "prepared", "accepted", "rechecking", "replayed", "failed"}
  /\ b \in {"initial", "prepared", "accepted", "rechecking", "replayed", "failed"}
  /\ committed \in BOOLEAN
  /\ resultA \in {"none", "fresh", "replay", "error"}
  /\ resultB \in {"none", "fresh", "replay", "error"}
AtMostOneFresh == ~(resultA = "fresh" /\ resultB = "fresh")
SameKeyConverges == (resultA # "none" /\ resultB # "none") =>
  ((resultA = "fresh" /\ resultB = "replay") \/ (resultB = "fresh" /\ resultA = "replay"))
THEOREM Spec => []TypeOK
====================================================
