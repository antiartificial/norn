package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"norn/v2/api/ingress"
)

func main() {
	listen := flag.String("listen", "", "private TCP listen address")
	routes := flag.String("routes", "/etc/traefik/dynamic", "managed route directory")
	traefik := flag.String("traefik", "http://127.0.0.1:18081", "local Traefik readback origin")
	certFile := flag.String("cert", "", "node TLS certificate")
	keyFile := flag.String("key", "", "node TLS private key")
	clientCAFile := flag.String("client-ca", "", "control client CA certificate")
	clientURI := flag.String("client-uri", "", "pinned control client URI SAN")
	flag.Parse()
	if *listen == "" || *certFile == "" || *keyFile == "" || *clientCAFile == "" || *clientURI == "" {
		log.Fatal("private listener and mutual TLS identity are required")
	}
	handler, err := ingress.NewNodeReadbackHandler(*routes, *traefik, *clientURI)
	if err != nil {
		log.Fatal(err)
	}
	certificate, err := tls.LoadX509KeyPair(*certFile, *keyFile)
	if err != nil {
		log.Fatalf("node TLS identity: %v", err)
	}
	caPEM, err := os.ReadFile(*clientCAFile)
	if err != nil {
		log.Fatalf("control client CA: %v", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		log.Fatal("control client CA is invalid")
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("private ingress listener: %v", err)
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{certificate},
			ClientAuth:   tls.RequireAndVerifyClientCert,
			ClientCAs:    roots,
		},
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("ingress observer shutdown: %v", err)
		}
	}()
	if err := server.Serve(tls.NewListener(listener, server.TLSConfig)); err != nil && err != http.ErrServerClosed {
		log.Fatal(fmt.Errorf("ingress observer: %w", err))
	}
}
