package etcdstore_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet"
	"norn/v2/api/ingress"
	"norn/v2/api/store"
)

func fleetInventoryTestCert(t *testing.T, parent *x509.Certificate, signer *ecdsa.PrivateKey, template *x509.Certificate) ([]byte, []byte, *x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if parent == nil {
		parent, signer = template, key
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private}), certificate, key
}

func probeActiveFleetInventoryPublishers(t *testing.T, nodes []ingress.IngressNode) {
	t.Helper()
	now := time.Now()
	root := x509.Certificate{SerialNumber: big.NewInt(81), Subject: pkix.Name{CommonName: "active inventory test CA"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caPEM, _, ca, caKey := fleetInventoryTestCert(t, nil, nil, &root)
	clientURI, _ := url.Parse("spiffe://norn.test/control/ingress-publisher")
	clientTemplate := x509.Certificate{SerialNumber: big.NewInt(82), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		URIs: []*url.URL{clientURI}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	clientPEM, clientKey, _, _ := fleetInventoryTestCert(t, ca, caKey, &clientTemplate)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("disposable inventory test CA is invalid")
	}
	for index, node := range nodes {
		parsed, err := url.Parse(node.APIURL)
		if err != nil || net.ParseIP(parsed.Hostname()) == nil {
			t.Fatal("active inventory node has no IP")
		}
		serverTemplate := x509.Certificate{SerialNumber: big.NewInt(int64(83 + index)), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
			IPAddresses: []net.IP{net.ParseIP(parsed.Hostname())}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		certPEM, keyPEM, _, _ := fleetInventoryTestCert(t, ca, caKey, &serverTemplate)
		identity, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			t.Fatal(err)
		}
		listener, err := net.Listen("tcp", net.JoinHostPort(parsed.Hostname(), "18083"))
		if err != nil {
			t.Fatal(err)
		}
		handler, err := ingress.NewNodePublisherHandler(t.TempDir(), clientURI.String(), node.ID,
			func(context.Context, string, string) (*ingress.AuthorizedRoutePublication, error) {
				return nil, fmt.Errorf("no route publication is authorized by inventory alone")
			})
		if err != nil {
			t.Fatal(err)
		}
		server := &http.Server{Handler: handler, ReadHeaderTimeout: 3 * time.Second}
		server.TLSConfig = &tls.Config{Certificates: []tls.Certificate{identity}, ClientAuth: tls.RequireAndVerifyClientCert,
			ClientCAs: roots, MinVersion: tls.VersionTLS12}
		go func() { _ = server.Serve(tls.NewListener(listener, server.TLSConfig)) }()
		t.Cleanup(func() { _ = server.Close() })
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := ingress.ProbePublisherNodesWithTLS(ctx, caPEM, clientPEM, clientKey, nodes, 18083); err != nil {
		t.Fatalf("active Fleet inventory did not reach exact mTLS publishers: %v", err)
	}
}

func TestV3CompletedFleetAttemptSelectsActiveIngressInventoryEtcd(t *testing.T) {
	adapter, client, prefix := fleetRunnerEtcdStore(t)
	ctx := context.Background()
	plan := fleetRunnerPlan(t, adapter, "scale")
	nonce := bindFleetRunnerDispatch(t, adapter, plan)
	accepted, err := adapter.Accept(ctx, fleetRunnerAcceptance(t, adapter, plan.ID, nonce, "ingress-attempt", "7", "apply", ""))
	if err != nil {
		t.Fatal(err)
	}
	attempt := *accepted.FleetRunnerAttempt
	addresses := []string{"10.43.0.21", "10.43.0.22"}
	if supplied := os.Getenv("NORN_TEST_INGRESS_PRIVATE_IPS"); supplied != "" {
		addresses = strings.Split(supplied, ",")
		if len(addresses) != 2 || addresses[0] == addresses[1] {
			t.Fatal("disposable ingress inventory requires two distinct private IPv4 addresses")
		}
		for _, address := range addresses {
			ip := net.ParseIP(address)
			if ip == nil || ip.To4() == nil || !ip.IsPrivate() || ip.IsLoopback() {
				t.Fatal("disposable ingress inventory address is not private IPv4")
			}
		}
	}
	snapshot := json.RawMessage(fmt.Sprintf(`{"cluster":"norn-staging","environment":"staging/nyc3","ingressNodes":[{"name":"ingress-01","privateIP":"%s"},{"name":"ingress-02","privateIP":"%s"}],"nodesFileSHA256":"%s","schemaVersion":"norn.fleet-ingress-inventory/v1"}`, addresses[0], addresses[1], strings.Repeat("c", 64)))
	canonical, err := fleet.CanonicalIngressInventory(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	phases := []string{"prechange_verified", "provider_applying", "infrastructure_applied", "inventory_generated", "nodes_configured", "nodes_enrolled", "readiness_verified", "complete"}
	for i, phase := range phases[:len(phases)-1] {
		if attempt.CurrentPhase != phase {
			t.Fatalf("attempt phase=%q, want %q", attempt.CurrentPhase, phase)
		}
		request := fleetReconciliationAcceptance(t, adapter, plan, attempt, "ingress-"+phase, phase)
		var checkpoint fleet.ReconciliationRequest
		encoded, err := json.Marshal(request.Operation.Payload)
		if err != nil || json.Unmarshal(encoded, &checkpoint) != nil {
			t.Fatal("decode checkpoint", err)
		}
		checkpoint.StateSerial = 7
		if phase == "nodes_configured" {
			checkpoint.IngressInventory = snapshot
			checkpoint.IngressInventoryDigest = digest
		}
		encoded, err = json.Marshal(checkpoint)
		if err != nil || json.Unmarshal(encoded, &request.Operation.Payload) != nil {
			t.Fatal("encode checkpoint", err)
		}
		request.Semantics["request"] = checkpoint
		request.Fingerprint, err = store.CanonicalOperationRequestFingerprint(request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := adapter.Accept(ctx, request); err != nil {
			t.Fatalf("accept %s checkpoint: %v", phase, err)
		}
		attemptResult, err := adapter.UpdateFleetRunnerAttempt(ctx, plan.ID, attempt.ID, attempt.Revision, "advance", phases[min(i+1, len(phases)-1)])
		if err != nil {
			t.Fatalf("advance %s: %v", phase, err)
		}
		attempt = *attemptResult
	}
	if attempt.Status != "succeeded" {
		t.Fatalf("attempt status=%q", attempt.Status)
	}
	proof, err := adapter.CurrentActiveFleetIngressInventory(ctx, "norn-staging", "staging/nyc3", 18082)
	if err != nil || proof.PlanID != plan.ID || proof.AttemptID != attempt.ID || proof.Digest != digest || proof.ActivePointerRevision <= 0 || proof.ActiveClusterEpochRevision <= 0 {
		t.Fatalf("active inventory=%+v err=%v", proof, err)
	}
	if len(proof.Nodes) != 2 || proof.Nodes[0].APIURL != "https://"+net.JoinHostPort(addresses[0], "18082") || proof.Nodes[1].APIURL != "https://"+net.JoinHostPort(addresses[1], "18082") {
		t.Fatalf("active inventory selected another private node set: %+v", proof.Nodes)
	}
	if os.Getenv("NORN_TEST_INGRESS_PRIVATE_IPS") != "" {
		probeActiveFleetInventoryPublishers(t, proof.Nodes)
	}
	if _, err := adapter.CurrentActiveFleetIngressInventory(ctx, "wrong-cluster", "staging/nyc3", 18082); err == nil {
		t.Fatal("unrelated cluster selected an active Fleet inventory")
	}
	if _, err := adapter.CurrentActiveFleetIngressInventory(ctx, "norn-staging", "wrong-environment", 18082); err == nil {
		t.Fatal("unrelated environment selected an active Fleet inventory")
	}
	clusterSum := sha256.Sum256([]byte("norn-staging"))
	epochKey := prefix + "/v3/fleet-active-ingress-cluster-epoch/" + hex.EncodeToString(clusterSum[:])
	if _, err := client.Put(ctx, epochKey, "other-plan"); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.CurrentActiveFleetIngressInventory(ctx, "norn-staging", "staging/nyc3", 18082); err == nil {
		t.Fatal("stale cluster generation retained an active Fleet inventory")
	}
	if _, err := client.Put(ctx, epochKey, plan.ID+"\x00"+attempt.ID); err != nil {
		t.Fatal(err)
	}
	checkpointKey := prefix + "/v3/fleet-reconciliations/" + plan.ID + "/" + proof.CheckpointID
	if _, err := client.Put(ctx, checkpointKey, `{}`); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.CurrentActiveFleetIngressInventory(ctx, "norn-staging", "staging/nyc3", 18082); err == nil {
		t.Fatal("changed inventory checkpoint was accepted after pointer publication")
	}
	legacyPlan := fleetRunnerPlan(t, adapter, "scale")
	legacyNonce := bindFleetRunnerDispatch(t, adapter, legacyPlan)
	legacyAccepted, err := adapter.Accept(ctx, fleetRunnerAcceptance(t, adapter, legacyPlan.ID, legacyNonce, "legacy-attempt", "7", "apply", ""))
	if err != nil {
		t.Fatal(err)
	}
	legacyAttempt := *legacyAccepted.FleetRunnerAttempt
	for i, phase := range phases[:len(phases)-1] {
		request := fleetReconciliationAcceptance(t, adapter, legacyPlan, legacyAttempt, "legacy-"+phase, phase)
		if _, err := adapter.Accept(ctx, request); err != nil {
			t.Fatalf("accept legacy %s checkpoint: %v", phase, err)
		}
		advanced, err := adapter.UpdateFleetRunnerAttempt(ctx, legacyPlan.ID, legacyAttempt.ID, legacyAttempt.Revision, "advance", phases[i+1])
		if err != nil {
			t.Fatalf("advance legacy %s: %v", phase, err)
		}
		legacyAttempt = *advanced
	}
	if legacyAttempt.Status != "succeeded" {
		t.Fatal("legacy Fleet plan did not finish")
	}
	active, err := client.Get(ctx, prefix+"/v3/fleet-active-ingress/", clientv3.WithPrefix())
	if err != nil || len(active.Kvs) != 0 {
		t.Fatalf("inventory pointer survived plan without ingress evidence: %d, %v", len(active.Kvs), err)
	}
}
