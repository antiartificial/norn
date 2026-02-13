package model

type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

type ValidationFinding struct {
	Check    string   `json:"check"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	Field    string   `json:"field,omitempty"`
}

type ValidationResult struct {
	App      string              `json:"app"`
	Errors   int                 `json:"errors"`
	Warnings int                 `json:"warnings"`
	Infos    int                 `json:"infos"`
	Findings []ValidationFinding `json:"findings"`
}

func (r *ValidationResult) Add(f ValidationFinding) {
	r.Findings = append(r.Findings, f)
	switch f.Severity {
	case SeverityError:
		r.Errors++
	case SeverityWarning:
		r.Warnings++
	case SeverityInfo:
		r.Infos++
	}
}

func (r *ValidationResult) Valid() bool {
	return r.Errors == 0
}
