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
	{"/v3/fleet-offline-dispatches/", ClassCurrentState, "immutable binding is permanently required to verify a DELETE commitment; export and restore with the commitment head"},
	{"/v3/fleet-offline-owner-consumptions/", ClassCurrentState, "one-use authorization tombstone; do not expire by approval validity or allow replay after restore"},
	{"/v3/fleet-offline-attempts/", ClassCurrentState, "admitted attempt is permanently required to verify a DELETE commitment; restore with binding and grant"},
	{"/v3/fleet-offline-checkpoints/", ClassCurrentState, "prechange checkpoint is permanently required to verify a DELETE commitment; restore with its event"},
	{"/v3/fleet-offline-checkpoint-events/", ClassCurrentState, "checkpoint event is permanently required to verify a DELETE commitment; restore with its head"},
	{"/v3/fleet-offline-effect-grants/", ClassCurrentState, "one-use effect grant cursor; restore with attempt, owner consumption, fence and epoch"},
	{"/v3/fleet-offline-effect-grant-events/", ClassCurrentState, "issue and consumption events are permanently required to verify a DELETE commitment; restore with consumed grant"},
	{"/v3/fleet-offline-terminals/", ClassHotEvidence, "terminal CAS cursor; restore with consumed grant, attempt, fence and epoch"},
	{"/v3/fleet-offline-terminal-events/", ClassHotEvidence, "append-only independently observed terminal proof; retain through recovery and archive qualification"},
	{"/v3/fleet-offline-delete-commitments/", ClassCurrentState, "permanent pre-dispatch DELETE tombstone; never expire or reconstruct after response loss; restore with binding, attempt, grant, checkpoint, fence and epoch"},
	{"/v3/fleet-offline-delete-commitment-events/", ClassCurrentState, "append-only exact commitment bytes are a permanent tombstone; restore with head and all referenced authority rows"},
}
