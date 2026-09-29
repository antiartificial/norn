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
	nodeID := flag.String("node-id", "", "Fleet ingress inventory node ID")
	certFile := flag.String("cert", "", "node publisher server TLS certificate")
	keyFile := flag.String("key", "", "node publisher server TLS private key")
	clientCAFile := flag.String("client-ca", "", "control publisher client CA certificate")
	clientURI := flag.String("client-uri", "", "pinned control publisher client URI SAN")
	authority := flag.String("authority", "", "private HTTPS control authority origin")
	authorityCAFile := flag.String("authority-ca", "", "control authority server CA certificate")
	authorityURI := flag.String("authority-uri", "", "pinned control authority server URI SAN")
	authorityCertFile := flag.String("authority-cert", "", "node authority client TLS certificate")
	authorityKeyFile := flag.String("authority-key", "", "node authority client TLS private key")
	flag.Parse()
	if *listen == "" || *nodeID == "" || *certFile == "" || *keyFile == "" || *clientCAFile == "" || *clientURI == "" || *authority == "" || *authorityCAFile == "" || *authorityURI == "" || *authorityCertFile == "" || *authorityKeyFile == "" {
		log.Fatal("private publisher and separate mutual TLS identities are required")
	}
	host, _, err := net.SplitHostPort(*listen)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || (!ip.IsPrivate() && !ip.IsLoopback()) {
		log.Fatal("publisher must listen on an explicit private IP")
	}
	authorityCA, err := os.ReadFile(*authorityCAFile)
	if err != nil {
		log.Fatal(err)
	}
	authorityCert, err := os.ReadFile(*authorityCertFile)
	if err != nil {
		log.Fatal(err)
	}
	authorityKey, err := os.ReadFile(*authorityKeyFile)
	if err != nil {
		log.Fatal(err)
	}
	resolve, err := ingress.NewRemoteRoutePublicationAuthority(*authority, *authorityURI, authorityCA, authorityCert, authorityKey)
	if err != nil {
		log.Fatal(err)
	}
	handler, err := ingress.NewNodePublisherHandler(*routes, *clientURI, *nodeID, resolve)
	if err != nil {
		log.Fatal(err)
	}
	serverIdentity, err := tls.LoadX509KeyPair(*certFile, *keyFile)
	if err != nil {
		log.Fatalf("node publisher TLS identity: %v", err)
	}
	clientCA, err := os.ReadFile(*clientCAFile)
	if err != nil {
		log.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(clientCA) {
		log.Fatal("control publisher client CA is invalid")
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("private publisher listener: %v", err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 1 << 16,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{serverIdentity}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("ingress publisher shutdown: %v", err)
		}
	}()
	if err := server.Serve(tls.NewListener(listener, server.TLSConfig)); err != nil && err != http.ErrServerClosed {
		log.Fatal(fmt.Errorf("ingress publisher: %w", err))
	}
}
