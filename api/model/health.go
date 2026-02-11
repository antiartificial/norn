package model

import "time"

type HealthCheck struct {
	ID         string    `json:"id"`
	App        string    `json:"app"`
	Healthy    bool      `json:"healthy"`
	ResponseMs int       `json:"responseMs"`
	CheckedAt  time.Time `json:"checkedAt"`
}

type AlertConfig struct {
	Window    string `yaml:"window" json:"window"`       // e.g. "5m"
	Threshold int    `yaml:"threshold" json:"threshold"` // failures in window
}
