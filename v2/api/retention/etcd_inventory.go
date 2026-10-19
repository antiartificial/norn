package retention

// EtcdPayloadInventory records retention decisions for the offline Fleet
// authority keys. PostgreSQL's control-recovery table registry does not
// enumerate etcd keys; these keys are exported together in full etcd
// snapshots and must be restored with the shared target fence and epoch.
type EtcdPayloadRetention struct {
	Prefix string
	Class  string
	Holds  string
}

var OfflineEtcdPayloadInventory = []EtcdPayloadRetention{
	{"/v3/fleet-offline-dispatches/", ClassHotEvidence, "immutable source, plan, backend, target and owner binding; retain while attempt, recovery, or audit can reference it"},
	{"/v3/fleet-offline-owner-consumptions/", ClassCurrentState, "one-use authorization tombstone; do not expire by approval validity or allow replay after restore"},
	{"/v3/fleet-offline-attempts/", ClassHotEvidence, "queued attempt and binding digest; retain through terminal recovery and archive qualification"},
	{"/v3/fleet-offline-checkpoints/", ClassHotEvidence, "CAS cursor for non-effect runner observations; restore with attempt, fence and epoch"},
	{"/v3/fleet-offline-checkpoint-events/", ClassHotEvidence, "append-only observation events; retain through terminal recovery and archive qualification"},
	{"/v3/fleet-offline-effect-grants/", ClassCurrentState, "one-use effect grant cursor; restore with attempt, owner consumption, fence and epoch"},
	{"/v3/fleet-offline-effect-grant-events/", ClassHotEvidence, "append-only issue and consumption events; retain through terminal recovery and archive qualification"},
	{"/v3/fleet-offline-terminals/", ClassHotEvidence, "terminal CAS cursor; restore with consumed grant, attempt, fence and epoch"},
	{"/v3/fleet-offline-terminal-events/", ClassHotEvidence, "append-only independently observed terminal proof; retain through recovery and archive qualification"},
}
