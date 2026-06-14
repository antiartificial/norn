package model

import "time"

type BeaconSeverity string

const (
	BeaconInfo     BeaconSeverity = "info"
	BeaconWarning  BeaconSeverity = "warning"
	BeaconCritical BeaconSeverity = "critical"
)

type BeaconEvent struct {
	ID                  string                 `json:"id"`
	Source              string                 `json:"source"`
	App                 string                 `json:"app,omitempty"`
	Environment         string                 `json:"environment,omitempty"`
	Type                string                 `json:"type"`
	Severity            BeaconSeverity         `json:"severity"`
	State               string                 `json:"state,omitempty"`
	Title               string                 `json:"title"`
	Body                string                 `json:"body"`
	DedupeKey           string                 `json:"dedupeKey"`
	OccurredAt          time.Time              `json:"occurredAt"`
	AcknowledgedAt      *time.Time             `json:"acknowledgedAt,omitempty"`
	AcknowledgedBy      string                 `json:"acknowledgedBy,omitempty"`
	AcknowledgementNote string                 `json:"acknowledgementNote,omitempty"`
	SnoozedUntil        *time.Time             `json:"snoozedUntil,omitempty"`
	Metadata            map[string]interface{} `json:"metadata,omitempty"`
}

type BeaconSinkStatus struct {
	Configured bool   `json:"configured"`
	URL        string `json:"url,omitempty"`
	KeyID      string `json:"keyId,omitempty"`
}

type NotificationChannel struct {
	ID         string   `json:"id"`
	Provider   string   `json:"provider"` // discord, ntfy, pushover
	Name       string   `json:"name"`
	URL        string   `json:"url"`
	Token      string   `json:"token,omitempty"`
	UserKey    string   `json:"userKey,omitempty"`
	Severities []string `json:"severities,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}
