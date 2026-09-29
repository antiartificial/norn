package main

import (
	"net/http"

	"norn/v2/api/handler"
)

// functionInvocationRoute exposes only the signed function-v3 admission
// boundary. The retained legacy handler is read-only history support and is
// deliberately not a submission fallback.
func functionInvocationRoute(admission http.HandlerFunc) http.HandlerFunc {
	if admission != nil {
		return admission
	}
	return func(w http.ResponseWriter, r *http.Request) {
		handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "function_invocation_unavailable", "signed function invocation is not configured")
	}
}
