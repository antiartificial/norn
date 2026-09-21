package handler

import (
	"net/http"
	"sort"

	"github.com/google/uuid"

	"norn/v2/api/model"
)

// appActivityEvent builds an operator-facing beacon event for a discrete action
// taken on an app (restart, scale, secret/config change). These are distinct
// from the admin mutation-audit receipts: they ride the api:read beacon stream
// so operators — not just admins — can see what happened to their workloads.
//
// The DedupeKey is unique per call: unlike health beacons, an operator-action
// ledger must record every occurrence (the beacon service otherwise collapses
// identical DedupeKeys within an hour, which would hide a second restart).
func appActivityEvent(app, eventType, title, body, actor string, metadata map[string]interface{}) model.BeaconEvent {
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	if actor != "" {
		metadata["actor"] = actor
	}
	return model.BeaconEvent{
		App:       app,
		Type:      eventType,
		Severity:  model.BeaconInfo,
		Title:     title,
		Body:      body,
		DedupeKey: eventType + ":" + app + ":" + uuid.NewString(),
		Metadata:  metadata,
	}
}

// emitAppActivity records an operator action as a beacon event, attributing the
// actor from the request principal. No-op when the beacon service is absent.
func (h *Handler) emitAppActivity(r *http.Request, app, eventType, title, body string, metadata map[string]interface{}) {
	if h.beacon == nil {
		return
	}
	actor := ""
	if principal, ok := AccessPrincipalFromRequest(r); ok {
		actor = principal.Subject
	}
	_, _ = h.beacon.Emit(r.Context(), appActivityEvent(app, eventType, title, body, actor, metadata))
}

// secretKeyNames returns the sorted key names of a secret map. It never returns
// values — secret activity records which keys changed, not their contents.
func secretKeyNames(updates map[string]string) []string {
	keys := make([]string, 0, len(updates))
	for k := range updates {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
