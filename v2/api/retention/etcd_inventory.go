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
	{"/v3/fleet-offline-effect-grants/", ClassCurrentState, "one-use inert grant cursor; restore with its attempt, owner consumption, shared target fence and epoch"},
	{"/v3/fleet-offline-effect-grant-events/", ClassCurrentState, "immutable issue and consume events; restore with the grant cursor for exact GET recovery"},
	{"/v3/fleet-offline-effect-v2/", ClassCurrentState, "effect v2 cursor; restore with owner, target fence, writer fence and complete one-shot lifecycle"},
	{"/v3/fleet-offline-effect-v2-events/", ClassCurrentState, "immutable issue, commitment, dispatch and terminal events; incomplete history never reauthorizes DELETE"},
}
