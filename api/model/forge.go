package model

import "time"

type ForgeStatus string

const (
	ForgeUnforged    ForgeStatus = "unforged"
	ForgeForging     ForgeStatus = "forging"
	ForgeForged      ForgeStatus = "forged"
	ForgeFailed      ForgeStatus = "forge_failed"
	ForgeTearingDown ForgeStatus = "tearing_down"
)

type ForgeState struct {
	App        string         `json:"app"`
	Status     ForgeStatus    `json:"status"`
	Steps      []ForgeStepLog `json:"steps"`
	Resources  ForgeResources `json:"resources"`
	Error      string         `json:"error,omitempty"`
	StartedAt  *time.Time     `json:"startedAt,omitempty"`
	FinishedAt *time.Time     `json:"finishedAt,omitempty"`
}

type ForgeStepLog struct {
	Step       string `json:"step"`
	Status     string `json:"status"` // running | completed | skipped | failed
	DurationMs int64  `json:"durationMs,omitempty"`
	Output     string `json:"output,omitempty"`
}

type ForgeResources struct {
	DeploymentName string `json:"deploymentName,omitempty"`
	DeploymentNS   string `json:"deploymentNs,omitempty"`
	ServiceName    string `json:"serviceName,omitempty"`
	ServiceNS      string `json:"serviceNs,omitempty"`
	ExternalHost   string `json:"externalHost,omitempty"`
	InternalHost   string `json:"internalHost,omitempty"`
	CloudflaredRule bool  `json:"cloudflaredRule,omitempty"`
	DNSRoute       bool   `json:"dnsRoute,omitempty"`
}

func (s ForgeStatus) IsTerminal() bool {
	return s == ForgeUnforged || s == ForgeForged || s == ForgeFailed
}

func (s ForgeStatus) CanForge() bool {
	return s == ForgeUnforged || s == ForgeFailed
}

func (s ForgeStatus) CanTeardown() bool {
	return s == ForgeForged || s == ForgeFailed
}
