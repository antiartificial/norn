package model

// CronState represents the state of a periodic process.
type CronState struct {
	App      string `json:"app"`
	Process  string `json:"process"`
	Paused   bool   `json:"paused"`
	Schedule string `json:"schedule"`
}
