package ingress

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"time"
)

// ServePrivateControlRouteAuthority serves a worker-held route decision on a
// private TCP listener. The caller retains the listener address and must stop
// this service when its operation or app lock ends. Each request's resolver
// still checks the durable claim and lock, including during shutdown races.
func ServePrivateControlRouteAuthority(ctx, lockCtx context.Context, listener net.Listener, handler http.Handler, certPEM, keyPEM, nodeCAPEM []byte) error {
	if ctx == nil || lockCtx == nil || ctx.Err() != nil || lockCtx.Err() != nil || listener == nil || handler == nil {
		return fmt.Errorf("claimed route authority listener is unavailable")
	}
	tlsConfig, err := PrivateControlRouteAuthorityTLS(listener, certPEM, keyPEM, nodeCAPEM)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 1 << 16, TLSConfig: tlsConfig}
	served := make(chan error, 1)
	go func() { served <- server.Serve(tls.NewListener(listener, server.TLSConfig)) }()
	select {
	case err := <-served:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case <-ctx.Done():
	case <-lockCtx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return err
	}
	<-served
	if lockCtx.Err() != nil {
		return fmt.Errorf("route authority app lock ended: %w", lockCtx.Err())
	}
	return ctx.Err()
}

// PrivateControlRouteAuthorityTLS checks the bound private address and mTLS
// material before a deployment is allowed to submit a Nomad job. The server
// calls the same check again when it begins serving.
func PrivateControlRouteAuthorityTLS(listener net.Listener, certPEM, keyPEM, nodeCAPEM []byte) (*tls.Config, error) {
	if listener == nil {
		return nil, fmt.Errorf("claimed route authority listener is unavailable")
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || address.IP == nil || (!address.IP.IsPrivate() && !address.IP.IsLoopback()) || address.IP.IsUnspecified() {
		return nil, fmt.Errorf("route authority must use a private TCP listener")
	}
	identity, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("route authority TLS identity: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(nodeCAPEM) {
		return nil, fmt.Errorf("route authority node CA is invalid")
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{identity}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots}, nil
}
