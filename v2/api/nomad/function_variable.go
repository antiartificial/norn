package nomad

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"regexp"
	"strings"

	nomadapi "github.com/hashicorp/nomad/api"
)

const (
	functionInvocationOwnerItem   = "norn_function_invocation_owner"
	functionInvocationPrivateItem = "norn_function_invocation_private"
)

type FunctionInvocationVariableIdentity struct {
	Path        string
	OwnerMarker string
}

type FunctionInvocationVariableState string

const (
	FunctionInvocationVariableNotFound      FunctionInvocationVariableState = "not-found"
	FunctionInvocationVariableFound         FunctionInvocationVariableState = "found"
	FunctionInvocationVariableIndeterminate FunctionInvocationVariableState = "indeterminate"
)

// FunctionInvocationVariableObservation deliberately returns only the path,
// owner marker, and transient private bytes that a worker must reconcile.
type FunctionInvocationVariableObservation struct {
	State          FunctionInvocationVariableState
	Path           string
	OwnerMarker    string
	PrivateContent []byte
}

var functionInvocationVariablePath = regexp.MustCompile(`^norn/function-invocation/[0-9a-f]{40}$`)

// These errors deliberately carry no Nomad response body. A response may have
// echoed the private variable content, so callers must use errors.Is rather
// than surfacing an underlying API error.
var (
	ErrFunctionVariableIdentity            = errors.New("function variable identity is invalid")
	ErrFunctionVariableLookupIndeterminate = errors.New("function variable lookup is indeterminate")
	ErrFunctionVariableCreateConflict      = errors.New("function variable already exists")
	ErrFunctionVariableCreateIndeterminate = errors.New("function variable create is indeterminate")
)

// LookupFunctionInvocationVariable reads only the two items needed to
// reconcile a function invocation's reserved variable. The private bytes are
// copied for immediate worker-memory use; no other Variable fields or items
// escape this adapter. A 404 is a conclusive FunctionVariableNotFound. Every
// other API failure is indeterminate and never proves absence.
func (c *Client) LookupFunctionInvocationVariable(ctx context.Context, region string, identity FunctionInvocationVariableIdentity) (FunctionInvocationVariableObservation, error) {
	if err := validateFunctionVariableIdentity(identity); err != nil {
		return FunctionInvocationVariableObservation{State: FunctionInvocationVariableIndeterminate}, err
	}
	if c == nil || c.api == nil {
		return FunctionInvocationVariableObservation{State: FunctionInvocationVariableIndeterminate}, ErrFunctionVariableLookupIndeterminate
	}
	variable, _, err := c.api.Variables().Read(identity.Path, (&nomadapi.QueryOptions{Region: region}).WithContext(ctx))
	if err != nil {
		if errors.Is(err, nomadapi.ErrVariablePathNotFound) || nomadHTTPStatus(err) == http.StatusNotFound {
			return FunctionInvocationVariableObservation{State: FunctionInvocationVariableNotFound}, nil
		}
		return FunctionInvocationVariableObservation{State: FunctionInvocationVariableIndeterminate}, ErrFunctionVariableLookupIndeterminate
	}
	if variable == nil {
		return FunctionInvocationVariableObservation{State: FunctionInvocationVariableIndeterminate}, ErrFunctionVariableLookupIndeterminate
	}
	encodedPrivateContent, ok := variable.Items[functionInvocationPrivateItem]
	if !ok {
		return FunctionInvocationVariableObservation{State: FunctionInvocationVariableIndeterminate}, ErrFunctionVariableLookupIndeterminate
	}
	privateContent, err := base64.StdEncoding.DecodeString(encodedPrivateContent)
	if err != nil {
		// Existing private variables were written before the padded encoding
		// contract. Keep their exact-read recovery available during rollout.
		privateContent, err = base64.RawStdEncoding.DecodeString(encodedPrivateContent)
	}
	if err != nil {
		return FunctionInvocationVariableObservation{State: FunctionInvocationVariableIndeterminate}, ErrFunctionVariableLookupIndeterminate
	}
	return FunctionInvocationVariableObservation{
		State:          FunctionInvocationVariableFound,
		Path:           variable.Path,
		OwnerMarker:    variable.Items[functionInvocationOwnerItem],
		PrivateContent: append([]byte(nil), privateContent...),
	}, nil
}

// CreateFunctionInvocationVariable creates the reserved private variable with
// Nomad CAS index zero. It never updates or merges an existing variable. A CAS
// conflict proves an existing value and is distinct from a transport or other
// ambiguous create outcome.
func (c *Client) CreateFunctionInvocationVariable(ctx context.Context, region string, identity FunctionInvocationVariableIdentity, privateContent []byte) error {
	if err := validateFunctionVariableIdentity(identity); err != nil {
		return err
	}
	if c == nil || c.api == nil {
		return ErrFunctionVariableCreateIndeterminate
	}
	variable := &nomadapi.Variable{
		Path: identity.Path,
		Items: nomadapi.VariableItems{
			functionInvocationOwnerItem:   identity.OwnerMarker,
			functionInvocationPrivateItem: base64.StdEncoding.EncodeToString(privateContent),
		},
	}
	_, _, err := c.api.Variables().CheckedCreate(variable, (&nomadapi.WriteOptions{Region: region}).WithContext(ctx))
	if err == nil {
		return nil
	}
	var conflict nomadapi.ErrCASConflict
	if errors.As(err, &conflict) || nomadHTTPStatus(err) == http.StatusConflict {
		return ErrFunctionVariableCreateConflict
	}
	return ErrFunctionVariableCreateIndeterminate
}

func validateFunctionVariableIdentity(identity FunctionInvocationVariableIdentity) error {
	if !functionInvocationVariablePath.MatchString(identity.Path) || strings.TrimSpace(identity.OwnerMarker) == "" || strings.ContainsAny(identity.OwnerMarker, "\r\n\x00") {
		return ErrFunctionVariableIdentity
	}
	return nil
}

func nomadHTTPStatus(err error) int {
	var response nomadapi.UnexpectedResponseError
	if errors.As(err, &response) && response.HasStatusCode() {
		return response.StatusCode()
	}
	return 0
}
