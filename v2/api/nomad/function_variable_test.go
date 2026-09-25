package nomad

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"
)

const functionVariableCanary = "NORN_FUNCTION_PRIVATE_9a71"

func functionVariableIdentity(t *testing.T) FunctionInvocationVariableIdentity {
	t.Helper()
	return FunctionInvocationVariableIdentity{Path: "norn/function-invocation/" + strings.Repeat("a", 40), OwnerMarker: "norn.function-invoke/operation-123"}
}

func TestFunctionInvocationVariableLookupAndCreateAreBounded(t *testing.T) {
	identity := functionVariableIdentity(t)
	private := append([]byte(functionVariableCanary+"\nmultiline\x00"), 0xff)
	var stored *nomadapi.Variable
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/var/"+identity.Path {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Header.Get("X-Nomad-Token") != "" {
			t.Fatal("test must not need an ACL token")
		}
		switch r.Method {
		case http.MethodGet:
			if stored == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(stored)
		case http.MethodPut:
			if r.URL.Query().Get("cas") != "0" {
				t.Fatalf("CAS = %q, want 0", r.URL.Query().Get("cas"))
			}
			var submitted nomadapi.Variable
			if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil {
				t.Fatal(err)
			}
			if stored != nil {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(stored)
				return
			}
			stored = &submitted
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(stored)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	observed, err := client.LookupFunctionInvocationVariable(context.Background(), "global", identity)
	if err != nil || observed.State != FunctionInvocationVariableNotFound || observed.Path != "" || observed.OwnerMarker != "" || observed.PrivateContent != nil {
		t.Fatalf("absent observation = %+v, %v", observed, err)
	}
	if err := client.CreateFunctionInvocationVariable(context.Background(), "global", identity, private); err != nil {
		t.Fatal(err)
	}
	if got := stored.Items[functionInvocationOwnerItem]; got != identity.OwnerMarker {
		t.Fatalf("owner = %q", got)
	}
	if got := stored.Items[functionInvocationPrivateItem]; got != base64.StdEncoding.EncodeToString(private) {
		t.Fatal("private content was not written")
	}
	observed, err = client.LookupFunctionInvocationVariable(context.Background(), "global", identity)
	if err != nil || observed.State != FunctionInvocationVariableFound || observed.Path != identity.Path || observed.OwnerMarker != identity.OwnerMarker || string(observed.PrivateContent) != string(private) {
		t.Fatalf("found observation = %+v, %v", observed, err)
	}
	observed.PrivateContent[0] = 'X'
	if stored.Items[functionInvocationPrivateItem] != base64.StdEncoding.EncodeToString(private) {
		t.Fatal("private content escaped without a copy")
	}
	stored.Items[functionInvocationPrivateItem] = base64.RawStdEncoding.EncodeToString(private)
	legacy, err := client.LookupFunctionInvocationVariable(context.Background(), "global", identity)
	if err != nil || legacy.State != FunctionInvocationVariableFound || string(legacy.PrivateContent) != string(private) {
		t.Fatalf("legacy unpadded variable could not be reconciled: %+v, %v", legacy, err)
	}
	if err := client.CreateFunctionInvocationVariable(context.Background(), "global", identity, private); !errors.Is(err, ErrFunctionVariableCreateConflict) {
		t.Fatalf("duplicate create = %v", err)
	}
}

func TestFunctionInvocationVariableErrorsDoNotExposePrivateContent(t *testing.T) {
	identity := functionVariableIdentity(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method == http.MethodGet {
			http.Error(w, functionVariableCanary, http.StatusBadGateway)
			return
		}
		if !strings.Contains(string(body), base64.StdEncoding.EncodeToString([]byte(functionVariableCanary))) {
			t.Fatal("test did not send private content")
		}
		http.Error(w, functionVariableCanary, http.StatusBadGateway)
	}))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := client.LookupFunctionInvocationVariable(context.Background(), "global", identity)
	if !errors.Is(err, ErrFunctionVariableLookupIndeterminate) || observed.State != FunctionInvocationVariableIndeterminate || strings.Contains(err.Error(), functionVariableCanary) {
		t.Fatalf("lookup = %+v, %v", observed, err)
	}
	err = client.CreateFunctionInvocationVariable(context.Background(), "global", identity, []byte(functionVariableCanary))
	if !errors.Is(err, ErrFunctionVariableCreateIndeterminate) || strings.Contains(err.Error(), functionVariableCanary) {
		t.Fatalf("create = %v", err)
	}
}

func TestFunctionInvocationVariableRejectsForgedIdentityBeforeRemoteCall(t *testing.T) {
	identity := functionVariableIdentity(t)
	identity.Path = "norn/function-invocation/forged"
	client, err := NewClient("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.LookupFunctionInvocationVariable(context.Background(), "global", identity); !errors.Is(err, ErrFunctionVariableIdentity) {
		t.Fatalf("lookup forged identity = %v", err)
	}
	if err := client.CreateFunctionInvocationVariable(context.Background(), "global", identity, []byte(functionVariableCanary)); !errors.Is(err, ErrFunctionVariableIdentity) || strings.Contains(err.Error(), functionVariableCanary) {
		t.Fatalf("create forged identity = %v", err)
	}
}

func TestFunctionInvocationVariableNilClientFailsClosed(t *testing.T) {
	identity := functionVariableIdentity(t)
	var client *Client
	observed, err := client.LookupFunctionInvocationVariable(context.Background(), "global", identity)
	if !errors.Is(err, ErrFunctionVariableLookupIndeterminate) || observed.State != FunctionInvocationVariableIndeterminate {
		t.Fatalf("nil lookup = %+v, %v", observed, err)
	}
	if err := client.CreateFunctionInvocationVariable(context.Background(), "global", identity, []byte(functionVariableCanary)); !errors.Is(err, ErrFunctionVariableCreateIndeterminate) || strings.Contains(err.Error(), functionVariableCanary) {
		t.Fatalf("nil create = %v", err)
	}
}
