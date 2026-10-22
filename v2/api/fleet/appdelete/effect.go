// Package appdelete defines the fail-closed authorization contract for one
// Fleet executor application-node delete. It performs no provider operation.
package appdelete

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"

	"github.com/google/uuid"
)

const (
	RequestSchema         = "norn.fleet-executor-app-delete-effect-request/v1"
	GrantSchema           = "norn.fleet-executor-app-delete-effect/v1"
	TerminalRequestSchema = "norn.fleet-app-delete-terminal-request/v1"
	TerminalReceiptSchema = "norn.fleet-app-delete-terminal-receipt/v1"
)

type TerminalRequest struct {
	SchemaVersion         string `json:"schemaVersion"`
	PlanID                string `json:"planId"`
	AttemptID             string `json:"attemptId"`
	WriterGeneration      uint64 `json:"writerGeneration"`
	WriterHolder          string `json:"writerHolder"`
	Target                string `json:"target"`
	DropletID             string `json:"dropletId"`
	ProviderAccount       string `json:"providerAccount"`
	AuthorizationSHA256   string `json:"authorizationSha256"`
	WorkloadReceiptSHA256 string `json:"workloadReceiptSha256"`
	DeleteRequestSHA256   string `json:"deleteRequestSha256"`
	RefreshDispatchSHA256 string `json:"refreshDispatchSha256"`
	StateSHA256           string `json:"stateSha256"`
	ProviderAbsenceSHA256 string `json:"providerAbsenceSha256"`
	WorkloadSHA256        string `json:"workloadSha256"`
	PublicPilotSHA256     string `json:"publicPilotSha256"`
	ObservationSHA256     string `json:"observationSha256"`
}

func (r TerminalRequest) Permit() Request {
	return Request{SchemaVersion: RequestSchema, PlanID: r.PlanID, AttemptID: r.AttemptID, WriterGeneration: r.WriterGeneration, WriterHolder: r.WriterHolder, Target: r.Target, DropletID: r.DropletID, ProviderAccount: r.ProviderAccount, AuthorizationSHA256: r.AuthorizationSHA256, WorkloadReceiptSHA256: r.WorkloadReceiptSHA256, DeleteRequestSHA256: r.DeleteRequestSHA256}
}
func (r TerminalRequest) Validate() error {
	if r.SchemaVersion != TerminalRequestSchema || r.Permit().Validate() != nil ||
		!hex64.MatchString(r.RefreshDispatchSHA256) || !hex64.MatchString(r.StateSHA256) ||
		!hex64.MatchString(r.ProviderAbsenceSHA256) || !hex64.MatchString(r.WorkloadSHA256) ||
		!hex64.MatchString(r.PublicPilotSHA256) || !hex64.MatchString(r.ObservationSHA256) {
		return ErrInvalid
	}
	return nil
}

type TerminalReceipt struct {
	SchemaVersion            string          `json:"schemaVersion"`
	TerminalEvidenceRecorded bool            `json:"terminalEvidenceRecorded"`
	Evidence                 TerminalRequest `json:"evidence"`
}

func ReceiptFromTerminal(r TerminalRequest) TerminalReceipt {
	return TerminalReceipt{SchemaVersion: TerminalReceiptSchema, TerminalEvidenceRecorded: true, Evidence: r}
}

func DecodeTerminalRequest(raw []byte) (TerminalRequest, error) {
	var request TerminalRequest
	if len(raw) == 0 || len(raw) > 16*1024 || !json.Valid(raw) || duplicateJSONKey(json.NewDecoder(bytes.NewReader(raw))) {
		return request, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF || request.Validate() != nil {
		return TerminalRequest{}, ErrInvalid
	}
	return request, nil
}

var (
	hex40       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	hex64       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	decimal     = regexp.MustCompile(`^[1-9][0-9]*$`)
	ErrInvalid  = errors.New("app delete effect request is invalid")
	ErrRefused  = errors.New("app delete effect authorization refused")
	ErrNotFound = errors.New("app delete terminal evidence not found")
)

// Request is the complete tuple presented by the attempt-bound executor.
// Norn compares its own attempt and fence facts in one CAS transaction.
// Fleet's separate PostgreSQL verifier and reservation must compare the
// writer, drain, reviewed plan and provider target facts before any DELETE.
type Request struct {
	SchemaVersion         string `json:"schemaVersion"`
	PlanID                string `json:"planId"`
	AttemptID             string `json:"attemptId"`
	WriterGeneration      uint64 `json:"writerGeneration"`
	WriterHolder          string `json:"writerHolder"`
	Target                string `json:"target"`
	DropletID             string `json:"dropletId"`
	ProviderAccount       string `json:"providerAccount"`
	AuthorizationSHA256   string `json:"authorizationSha256"`
	WorkloadReceiptSHA256 string `json:"workloadReceiptSha256"`
	DeleteRequestSHA256   string `json:"deleteRequestSha256"`
}

func (r Request) Validate() error {
	if r.SchemaVersion != RequestSchema || uuid.Validate(r.PlanID) != nil || uuid.Validate(r.AttemptID) != nil ||
		r.WriterGeneration == 0 || uuid.Validate(r.WriterHolder) != nil ||
		(r.Target != "app-03" && r.Target != "app-04") || !decimal.MatchString(r.DropletID) ||
		uuid.Validate(r.ProviderAccount) != nil || !hex64.MatchString(r.AuthorizationSHA256) ||
		!hex64.MatchString(r.WorkloadReceiptSHA256) || r.DeleteRequestSHA256 != ExactDeleteRequestSHA256(r.DropletID) {
		return ErrInvalid
	}
	return nil
}

// DecodeRequest rejects unknown fields, duplicate keys and trailing values.
func DecodeRequest(raw []byte) (Request, error) {
	var request Request
	if len(raw) == 0 || len(raw) > 16*1024 || !json.Valid(raw) || duplicateJSONKey(json.NewDecoder(bytes.NewReader(raw))) {
		return request, ErrInvalid
	}
	var keys map[string]json.RawMessage
	if json.Unmarshal(raw, &keys) != nil || !exactRequestKeys(keys) {
		return Request{}, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF || request.Validate() != nil {
		return Request{}, ErrInvalid
	}
	return request, nil
}

func exactRequestKeys(keys map[string]json.RawMessage) bool {
	want := []string{"schemaVersion", "planId", "attemptId", "writerGeneration", "writerHolder", "target", "dropletId", "providerAccount", "authorizationSha256", "workloadReceiptSha256", "deleteRequestSha256"}
	if len(keys) != len(want) {
		return false
	}
	for _, key := range want {
		if _, ok := keys[key]; !ok {
			return false
		}
	}
	return true
}

// Grant intentionally matches the executor's exact-key equality contract.
// It has no grant ID or expiry that could tempt a caller to replay it.
type Grant struct {
	SchemaVersion         string `json:"schemaVersion"`
	AuthorizesEffect      bool   `json:"authorizesEffect"`
	PlanID                string `json:"planId"`
	AttemptID             string `json:"attemptId"`
	WriterGeneration      uint64 `json:"writerGeneration"`
	WriterHolder          string `json:"writerHolder"`
	Target                string `json:"target"`
	DropletID             string `json:"dropletId"`
	ProviderAccount       string `json:"providerAccount"`
	AuthorizationSHA256   string `json:"authorizationSha256"`
	WorkloadReceiptSHA256 string `json:"workloadReceiptSha256"`
	DeleteRequestSHA256   string `json:"deleteRequestSha256"`
}

// GrantFromRequest is only Norn's one-use attempt co-permit. Its copied
// PostgreSQL and provider fields are opaque bindings, not Norn attestations.
func GrantFromRequest(r Request) Grant {
	return Grant{SchemaVersion: GrantSchema, AuthorizesEffect: true,
		PlanID: r.PlanID, AttemptID: r.AttemptID,
		WriterGeneration: r.WriterGeneration, WriterHolder: r.WriterHolder,
		Target: r.Target, DropletID: r.DropletID, ProviderAccount: r.ProviderAccount,
		AuthorizationSHA256:   r.AuthorizationSHA256,
		WorkloadReceiptSHA256: r.WorkloadReceiptSHA256,
		DeleteRequestSHA256:   r.DeleteRequestSHA256}
}

// ExactDeleteRequestSHA256 reproduces the executor's canonical provider
// request digest. Norn does not accept a caller-selected mutation shape.
func ExactDeleteRequestSHA256(dropletID string) string {
	if !decimal.MatchString(dropletID) {
		return ""
	}
	empty := sha256.Sum256(nil)
	raw := []byte(`{"apiVersion":"v2","bodySHA256":"` + hex.EncodeToString(empty[:]) + `","method":"DELETE","origin":"https://api.digitalocean.com","path":"/v2/droplets/` + dropletID + `"}` + "\n")
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func duplicateJSONKey(decoder *json.Decoder) bool {
	token, err := decoder.Token()
	if err != nil {
		return true
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return false
	}
	seen := map[string]bool{}
	for decoder.More() {
		if delim == '{' {
			key, err := decoder.Token()
			if err != nil {
				return true
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return true
			}
			seen[name] = true
		}
		if duplicateJSONKey(decoder) {
			return true
		}
	}
	_, err = decoder.Token()
	return err != nil
}

// Keep commit SHA validation available to durable backend implementations
// that bind this tuple to the controller's reviewed attempt.
func ValidCommitSHA(value string) bool { return hex40.MatchString(value) }
