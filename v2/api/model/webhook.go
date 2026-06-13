package model

import "time"

type WebhookDelivery struct {
	ID         string                 `json:"id"`
	Provider   string                 `json:"provider"`
	Event      string                 `json:"event,omitempty"`
	DeliveryID string                 `json:"deliveryId,omitempty"`
	Repository string                 `json:"repository,omitempty"`
	Ref        string                 `json:"ref,omitempty"`
	Branch     string                 `json:"branch,omitempty"`
	App        string                 `json:"app,omitempty"`
	SagaID     string                 `json:"sagaId,omitempty"`
	Status     string                 `json:"status"`
	Reason     string                 `json:"reason,omitempty"`
	RemoteAddr string                 `json:"remoteAddr,omitempty"`
	UserAgent  string                 `json:"userAgent,omitempty"`
	Payload    map[string]interface{} `json:"payload,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
	ReceivedAt time.Time              `json:"receivedAt"`
	UpdatedAt  time.Time              `json:"updatedAt"`
}
