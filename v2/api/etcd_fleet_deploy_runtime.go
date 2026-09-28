package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"norn/v2/api/config"
	"norn/v2/api/database"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleetdeploy"
	"norn/v2/api/ingress"
	"norn/v2/api/nomad"
	"norn/v2/api/secrets"
	"norn/v2/api/worker"
)

const etcdFleetDeployWorkerConfigEnv = "NORN_ETCD_FLEET_DEPLOY_WORKER_CONFIG"

type etcdFleetDeployWorkerConfig struct {
	Bind              string            `json:"bind"`
	NodeURIs          map[string]string `json:"nodeUris"`
	AuthorityCertFile string            `json:"authorityCertFile"`
	AuthorityKeyFile  string            `json:"authorityKeyFile"`
	NodeCAFile        string            `json:"nodeCaFile"`
	PublisherCAFile   string            `json:"publisherCaFile"`
	PublisherCertFile string            `json:"publisherCertFile"`
	PublisherKeyFile  string            `json:"publisherKeyFile"`
	ObserverCAFile    string            `json:"observerCaFile"`
	ObserverCertFile  string            `json:"observerCertFile"`
	ObserverKeyFile   string            `json:"observerKeyFile"`
	PublicCAFile      string            `json:"publicCaFile,omitempty"`
	ObserverPort      int               `json:"observerPort"`
	PublisherPort     int               `json:"publisherPort"`
	EndpointPort      int               `json:"endpointPort"`
}

type etcdFleetDeployRuntime struct {
	worker  *worker.OperationWorker
	secrets *database.DirectorySecretSource
}

func newEtcdFleetDeployRuntime(ctx context.Context, cfg *config.Config, operations *etcdstore.V3OperationStore,
	configPath string) (*etcdFleetDeployRuntime, error) {
	if cfg == nil || operations == nil || cfg.AppsDir == "" || cfg.ControlAuthority == "" ||
		cfg.DatabaseProfile == "" || cfg.DatabaseSecretDir == "" || configPath == "" {
		return nil, fmt.Errorf("etcd Fleet deploy worker requires app catalog, authority, database profile and private material")
	}
	encoded, err := privateFleetDeployFile(configPath)
	if err != nil {
		return nil, err
	}
	var document etcdFleetDeployWorkerConfig
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("etcd Fleet deploy worker config: %w", err)
	}
	var trailing json.RawMessage
	if decoder.Decode(&trailing) != io.EOF {
		return nil, fmt.Errorf("etcd Fleet deploy worker config has trailing content")
	}
	host, _, err := net.SplitHostPort(document.Bind)
	if err != nil || net.ParseIP(host) == nil {
		return nil, fmt.Errorf("etcd Fleet deploy authority bind must name a private IP and port")
	}
	address, err := net.ResolveTCPAddr("tcp", document.Bind)
	if err != nil || address.IP == nil || address.IP.IsUnspecified() || (!address.IP.IsPrivate() && !address.IP.IsLoopback()) || address.Port < 1 {
		return nil, fmt.Errorf("etcd Fleet deploy authority bind must name a private IP and port")
	}
	if document.ObserverPort < 1024 || document.ObserverPort > 65535 || document.PublisherPort < 1024 || document.PublisherPort > 65535 || document.PublisherPort == document.ObserverPort || document.EndpointPort < 1 || document.EndpointPort > 65535 || len(document.NodeURIs) == 0 {
		return nil, fmt.Errorf("etcd Fleet deploy route transport is incomplete")
	}
	if _, err := ingress.NewControlRouteAuthorityHandler(document.NodeURIs, func(context.Context, string, string) (*ingress.AuthorizedRoutePublication, error) {
		return nil, fmt.Errorf("route authority has not started")
	}); err != nil {
		return nil, err
	}
	transport := fleetdeploy.ClaimedFleetRouteTransport{NodeURIs: document.NodeURIs, ObserverPort: document.ObserverPort, PublisherPort: document.PublisherPort, EndpointPort: document.EndpointPort,
		Listen: func() (net.Listener, error) { return net.ListenTCP("tcp", address) }}
	files := []struct {
		path   string
		result *[]byte
	}{
		{document.AuthorityCertFile, &transport.AuthorityCertPEM}, {document.AuthorityKeyFile, &transport.AuthorityKeyPEM},
		{document.NodeCAFile, &transport.NodeCAPEM}, {document.PublisherCAFile, &transport.PublisherCAPEM},
		{document.PublisherCertFile, &transport.PublisherCertPEM}, {document.PublisherKeyFile, &transport.PublisherKeyPEM},
		{document.ObserverCAFile, &transport.ObserverCAPEM}, {document.ObserverCertFile, &transport.ObserverCertPEM},
		{document.ObserverKeyFile, &transport.ObserverKeyPEM},
	}
	for _, file := range files {
		*file.result, err = privateFleetDeployFile(file.path)
		if err != nil {
			return nil, err
		}
	}
	if document.PublicCAFile != "" {
		publicPEM, err := privateFleetDeployFile(document.PublicCAFile)
		if err != nil {
			return nil, err
		}
		transport.PublicRoots = x509.NewCertPool()
		if !transport.PublicRoots.AppendCertsFromPEM(publicPEM) {
			return nil, fmt.Errorf("etcd Fleet deploy public CA is invalid")
		}
	}
	listener, err := transport.Listen()
	if err != nil {
		return nil, err
	}
	_, err = ingress.PrivateControlRouteAuthorityTLS(listener, transport.AuthorityCertPEM, transport.AuthorityKeyPEM, transport.NodeCAPEM)
	_ = listener.Close()
	if err != nil {
		return nil, err
	}
	for _, identity := range []struct{ ca, cert, key []byte }{
		{transport.PublisherCAPEM, transport.PublisherCertPEM, transport.PublisherKeyPEM},
		{transport.ObserverCAPEM, transport.ObserverCertPEM, transport.ObserverKeyPEM},
	} {
		client, err := ingress.NewMutualTLSNodeClient(identity.ca, identity.cert, identity.key)
		if err != nil {
			return nil, err
		}
		client.CloseIdleConnections()
	}
	active, err := operations.ActiveDatabaseCatalog(ctx)
	if err != nil {
		return nil, err
	}
	profileFound := false
	for _, profile := range active.Catalog.Profiles {
		profileFound = profileFound || profile.ID == cfg.DatabaseProfile
	}
	if !profileFound {
		return nil, fmt.Errorf("etcd Fleet deploy database profile is not active")
	}
	privateSecrets, err := database.NewDirectorySecretSource(cfg.DatabaseSecretDir)
	if err != nil {
		return nil, err
	}
	nomadClient, err := nomad.NewClient(cfg.NomadAddr)
	if err != nil {
		_ = privateSecrets.Close()
		return nil, err
	}
	executor := &fleetdeploy.ClaimedFleetDeploymentExecutor{Store: operations, Nomad: nomadClient,
		DatabaseSecrets: privateSecrets, JobSecrets: secrets.NewManager(cfg.AppsDir), AppsDir: cfg.AppsDir,
		Authority: cfg.ControlAuthority, DatabaseProfile: cfg.DatabaseProfile, Route: transport}
	return &etcdFleetDeployRuntime{worker: worker.NewOperationWorkerForKinds(operations, executor, []string{"app.deploy"}), secrets: privateSecrets}, nil
}

// privateFleetDeployFile refuses symlinks, group/world access, and foreign
// ownership for every control certificate, private key and config document.
func privateFleetDeployFile(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("etcd Fleet deploy material path must be absolute")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("etcd Fleet deploy material must be an owner-only regular file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 1<<20 {
		return nil, fmt.Errorf("etcd Fleet deploy material must be an owner-only regular file")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != uint32(os.Geteuid()) {
		return nil, fmt.Errorf("etcd Fleet deploy material must be owned by this user")
	}
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, fmt.Errorf("etcd Fleet deploy material could not be read")
	}
	return data, nil
}
