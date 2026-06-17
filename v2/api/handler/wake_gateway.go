package handler

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

const (
	defaultWakeGatewayTimeout = 30 * time.Second
	maxWakeGatewayTimeout     = 2 * time.Minute
)

type wakeGatewayTarget struct {
	App      string
	Process  string
	Endpoint string
	Service  model.ServiceManifestEntry
}

func (h *Handler) WakeGateway(w http.ResponseWriter, r *http.Request) {
	hostname := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "host")))
	if hostname == "" {
		writeError(w, http.StatusBadRequest, "wake gateway hostname is required")
		return
	}

	manifest, err := h.buildServiceManifest()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	target, ok := wakeGatewayTargetForHost(manifest.Services, hostname)
	if !ok {
		writeError(w, http.StatusNotFound, "wake gateway hostname is not mapped to a service endpoint")
		return
	}

	status := http.StatusOK
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		h.recordWakeGatewayObservation(ctx, target, status)
	}()

	timeout := wakeGatewayTimeout(r)
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	instance, woke, err := h.ensureWakeGatewayReady(ctx, target)
	if err != nil {
		status = http.StatusGatewayTimeout
		w.Header().Set("Retry-After", "5")
		writeError(w, status, err.Error())
		return
	}
	if instance.Address == "" || instance.Port <= 0 {
		status = http.StatusServiceUnavailable
		w.Header().Set("Retry-After", "5")
		writeError(w, status, "service is not routable")
		return
	}

	upstream := &url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(instance.Address, strconv.Itoa(instance.Port)),
	}
	upstreamPath := wakeGatewayUpstreamPath(r, hostname)
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.Director = func(out *http.Request) {
		query := r.URL.Query()
		query.Del("wakeTimeout")
		out.URL.Scheme = upstream.Scheme
		out.URL.Host = upstream.Host
		out.URL.Path = upstreamPath
		out.URL.RawPath = ""
		out.URL.RawQuery = query.Encode()
		out.Host = r.Host
		if ip := clientIP(r); ip != "" {
			if prior := out.Header.Get("X-Forwarded-For"); prior != "" {
				out.Header.Set("X-Forwarded-For", prior+", "+ip)
			} else {
				out.Header.Set("X-Forwarded-For", ip)
			}
		}
		out.Header.Set("X-Forwarded-Host", r.Host)
		out.Header.Set("X-Forwarded-Proto", forwardedProto(r))
		out.Header.Set("X-Norn-Wake-Gateway", "true")
		out.Header.Set("X-Norn-Wake-App", target.App)
		out.Header.Set("X-Norn-Wake-Process", target.Process)
		if woke {
			out.Header.Set("X-Norn-Wake-Action", "scaled")
		} else {
			out.Header.Set("X-Norn-Wake-Action", "ready")
		}
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		status = resp.StatusCode
		resp.Header.Set("X-Norn-Wake-Gateway", "true")
		if woke {
			resp.Header.Set("X-Norn-Wake-Action", "scaled")
		} else {
			resp.Header.Set("X-Norn-Wake-Action", "ready")
		}
		return nil
	}
	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, proxyErr error) {
		status = http.StatusBadGateway
		rw.Header().Set("Retry-After", "5")
		writeError(rw, status, fmt.Sprintf("wake gateway proxy error: %v", proxyErr))
	}
	proxy.ServeHTTP(w, r)
}

func (h *Handler) ensureWakeGatewayReady(ctx context.Context, target wakeGatewayTarget) (model.ServiceInstance, bool, error) {
	key := target.App + "\x00" + target.Process
	lock := h.wakeGatewayLock(key)
	lock.Lock()
	defer lock.Unlock()

	if manifest, err := h.buildServiceManifest(); err == nil {
		if refreshed, ok := wakeGatewayTargetForHost(manifest.Services, endpointHostname(target.Endpoint)); ok {
			target = refreshed
		}
	}
	if instance, ok := firstReadyInstance(target.Service); ok {
		return instance, false, nil
	}
	if h.nomad == nil {
		return model.ServiceInstance{}, false, fmt.Errorf("nomad is not connected and %s/%s has no ready instance", target.App, target.Process)
	}
	if err := h.nomad.ScaleJob(target.App, target.Process, 1); err != nil {
		return model.ServiceInstance{}, false, fmt.Errorf("scale %s/%s to 1: %w", target.App, target.Process, err)
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return model.ServiceInstance{}, true, fmt.Errorf("timeout waiting for %s/%s to wake", target.App, target.Process)
		case <-ticker.C:
			manifest, err := h.buildServiceManifest()
			if err != nil {
				continue
			}
			refreshed, ok := wakeGatewayTargetForHost(manifest.Services, endpointHostname(target.Endpoint))
			if !ok {
				continue
			}
			if instance, ok := firstReadyInstance(refreshed.Service); ok {
				return instance, true, nil
			}
		}
	}
}

func (h *Handler) wakeGatewayLock(key string) *sync.Mutex {
	actual, _ := h.wakeLocks.LoadOrStore(key, &sync.Mutex{})
	return actual.(*sync.Mutex)
}

func wakeGatewayTargetForHost(services []model.ServiceManifestEntry, hostname string) (wakeGatewayTarget, bool) {
	hostname = strings.ToLower(strings.TrimSpace(hostname))
	for _, service := range services {
		if service.Type != "service" {
			continue
		}
		for _, endpoint := range service.Endpoints {
			if endpointHostname(endpoint.URL) != hostname {
				continue
			}
			return wakeGatewayTarget{
				App:      service.App,
				Process:  service.Process,
				Endpoint: endpoint.URL,
				Service:  service,
			}, true
		}
	}
	return wakeGatewayTarget{}, false
}

func firstReadyInstance(service model.ServiceManifestEntry) (model.ServiceInstance, bool) {
	for _, instance := range service.Instances {
		if instance.Address != "" && instance.Port > 0 && instance.Status == "passing" {
			return instance, true
		}
	}
	for _, instance := range service.Instances {
		if instance.Address != "" && instance.Port > 0 && instance.Status == "" {
			return instance, true
		}
	}
	return model.ServiceInstance{}, false
}

func (h *Handler) recordWakeGatewayObservation(ctx context.Context, target wakeGatewayTarget, status int) {
	if h == nil || h.db == nil {
		return
	}
	_ = h.db.RecordAccessObservation(ctx, store.AccessObservation{
		App:        target.App,
		Process:    target.Process,
		Endpoint:   target.Endpoint,
		Source:     "wake-gateway",
		ObservedAt: time.Now().UTC(),
		Count:      1,
		Status:     status,
	})
}

func wakeGatewayTimeout(r *http.Request) time.Duration {
	timeout := durationQuery(r, "wakeTimeout", defaultWakeGatewayTimeout)
	if timeout > maxWakeGatewayTimeout {
		return maxWakeGatewayTimeout
	}
	return timeout
}

func wakeGatewayUpstreamPath(r *http.Request, hostname string) string {
	prefix := "/api/wake-gateway/" + hostname
	path := strings.TrimPrefix(r.URL.Path, prefix)
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}
	return path
}

func forwardedProto(r *http.Request) string {
	if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); proto != "" {
		return proto
	}
	if visitor := strings.TrimSpace(r.Header.Get("CF-Visitor")); strings.Contains(visitor, "https") {
		return "https"
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}
