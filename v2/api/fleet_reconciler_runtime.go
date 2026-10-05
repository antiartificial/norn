package main

// Wiring for the Fleet resource reconciler (docs/v3/fleet-controller/plan.md
// §2.4, Q6, WP12). It runs only in the PG Fleet authority-only process and in
// the etcd Fleet runtime, never in the general router's process, and it only
// ever writes Fleet status through the store's reconcile CAS.

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5/middleware"

	"norn/v2/api/fleet/controller"
)

// fleetReconcilerEnv switches the Fleet resource reconciler. It is on by
// default in a Fleet process: unset or empty enables it, and a false value
// ("false", "0", "f", any case) disables it. The value is parsed with
// strconv.ParseBool (as config.envBoolOr does); an unparseable value is a
// startup error rather than silently leaving a kill switch ignored.
const fleetReconcilerEnv = "NORN_FLEET_RECONCILER"

func fleetReconcilerEnabled(getenv func(string) string) (bool, error) {
	raw := strings.TrimSpace(getenv(fleetReconcilerEnv))
	if raw == "" {
		return true, nil
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean (true or false), got %q", fleetReconcilerEnv, raw)
	}
	return enabled, nil
}

func newFleetReconciler(store controller.ReconcilerStore, isNotFound func(error) bool) *controller.Reconciler {
	return &controller.Reconciler{
		Store: store, IsNotFound: isNotFound,
		OnError: func(name string, err error, retrying bool) {
			log.Printf("WARNING: fleet reconciler resource=%q retrying=%t: %v", name, retrying, err)
		},
	}
}

// startFleetReconciler runs r until ctx is cancelled. The returned stop
// function cancels it and waits for Run to return.
func startFleetReconciler(ctx context.Context, r *controller.Reconciler) (stop func()) {
	runCtx, cancel := context.WithCancel(ctx)
	var done sync.WaitGroup
	done.Add(1)
	go func() {
		defer done.Done()
		r.Run(runCtx)
	}()
	return func() {
		cancel()
		done.Wait()
	}
}

// fleetReconcilerNotify enqueues work after a committed Fleet mutation: a
// request under /api/v1/fleet/ that was accepted (2xx) and is not a read.
// Plan, dispatch, checkpoint, attempt and target (fence) routes carry no
// resource name, so they request a coalesced early rescan; a resource route
// (/api/v1/fleet/resources/{name}/..., e.g. an observation append) enqueues
// that resource. A mutation that committed but failed to respond is caught
// by the periodic rescan.
func fleetReconcilerNotify(r *controller.Reconciler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, req.ProtoMajor)
			next.ServeHTTP(ww, req)
			if req.Method == http.MethodGet || req.Method == http.MethodHead || req.Method == http.MethodOptions {
				return
			}
			// Status 0 means the handler never wrote: net/http then sends an
			// implicit 200 (or the connection was hijacked). Notifying is
			// harmless, so only an explicit non-2xx suppresses it.
			if status := ww.Status(); status != 0 && (status < 200 || status >= 300) {
				return
			}
			const fleetPrefix = "/api/v1/fleet/"
			if !strings.HasPrefix(req.URL.Path, fleetPrefix) {
				return
			}
			rest := strings.TrimPrefix(req.URL.Path, fleetPrefix)
			if strings.HasPrefix(rest, "resources/") {
				if name, _, _ := strings.Cut(strings.TrimPrefix(rest, "resources/"), "/"); name != "" {
					r.Notify(name)
					return
				}
			}
			r.NotifyAll()
		})
	}
}
