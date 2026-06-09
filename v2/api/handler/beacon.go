package handler

import (
	"context"
	"log"

	"norn/v2/api/model"
)

func (h *Handler) emitBeacon(ctx context.Context, event model.BeaconEvent) {
	if h.beacon == nil {
		return
	}
	if _, err := h.beacon.Emit(ctx, event); err != nil {
		log.Printf("handler: beacon emit %s/%s: %v", event.App, event.Type, err)
	}
}
