package model

import "encoding/json"

// Operation read projection. Some accepted operations carry payload that is
// needed for execution and bound into the signed acceptance, but must not be
// served to readers: the database catalog activation carries the complete
// catalog (private endpoints and secret references). Every JSON rendering of
// an Operation (API get/list/active/cancel, replay responses, and anything
// else that encodes the struct) goes through ReadProjection. Storage and
// signing never encode this struct: they marshal the payload map directly and
// build canonical request material separately, so persisted and signed bytes
// are unchanged.

// catalogActivationKind mirrors pipeline.CatalogActivationKind (model cannot
// import pipeline).
const catalogActivationKind = "database.catalog-activate"

// projectedPayloadKeys lists, per sensitive kind, the only payload keys a
// reader may see. Kinds not listed are rendered unchanged.
var projectedPayloadKeys = map[string]map[string]bool{
	catalogActivationKind: {"expectedRevision": true, "catalogDigest": true, "requestedBy": true},
}

// ReadProjection returns a copy safe to serialize to a reader.
func (o Operation) ReadProjection() Operation {
	allowed, sensitive := projectedPayloadKeys[o.Kind]
	if !sensitive || o.Payload == nil {
		return o
	}
	projected := make(map[string]interface{}, len(allowed)+1)
	for key, value := range o.Payload {
		if allowed[key] {
			projected[key] = value
		}
	}
	projected["redacted"] = true
	o.Payload = projected
	return o
}

// MarshalJSON renders the read projection.
func (o Operation) MarshalJSON() ([]byte, error) {
	type wire Operation
	return json.Marshal(wire(o.ReadProjection()))
}
