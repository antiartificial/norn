#!/usr/bin/env python3
"""Rehearse the private Fleet inventory-to-publisher path in disposable Docker networks."""

import json
import os
import pathlib
import subprocess
import tempfile
import time
import uuid


API = pathlib.Path(__file__).resolve().parents[1]
IMAGE = "ubuntu:24.04"
ETCD_IMAGE = "quay.io/coreos/etcd:v3.5.17"


def run(*args, capture=False, **kwargs):
    return subprocess.run(args, check=True, text=True, capture_output=capture, **kwargs)


def main():
    run("docker", "image", "inspect", IMAGE, capture=True)
    run("docker", "image", "inspect", ETCD_IMAGE, capture=True)
    architecture = run("docker", "version", "--format", "{{.Server.Arch}}", capture=True).stdout.strip()
    if architecture not in {"amd64", "arm64"}:
        raise SystemExit(f"unsupported disposable Docker architecture: {architecture}")
    suffix = uuid.uuid4().hex[:12]
    first_network, second_network = (f"norn-v3-ingress-{side}-{suffix}" for side in ("a", "b"))
    container = f"norn-v3-ingress-test-{suffix}"
    etcd_container = f"norn-v3-ingress-etcd-{suffix}"
    created_networks = []
    started_container = False
    started_etcd = False
    with tempfile.TemporaryDirectory(prefix="norn-v3-ingress-") as temporary:
        binary = pathlib.Path(temporary) / "ingress.test"
        environment = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH=architecture)
        run("go", "test", "-c", "./ingress", "-o", str(binary), cwd=API, env=environment)
        etcd_binary = pathlib.Path(temporary) / "etcdstore.test"
        run("go", "test", "-c", "./etcdstore", "-o", str(etcd_binary), cwd=API, env=environment)
        try:
            for network in (first_network, second_network):
                run("docker", "network", "create", network, capture=True)
                created_networks.append(network)
            run(
                "docker", "run", "-d", "--name", container, "--network", first_network,
                "--mount", f"type=bind,src={binary},dst=/work/ingress.test,readonly",
                "--mount", f"type=bind,src={etcd_binary},dst=/work/etcdstore.test,readonly",
                "--entrypoint", "sleep", IMAGE, "120", capture=True,
            )
            started_container = True
            run("docker", "network", "connect", second_network, container)
            run(
                "docker", "run", "-d", "--name", etcd_container, "--network", first_network,
                ETCD_IMAGE, "/usr/local/bin/etcd", "--name", "disposable",
                "--listen-client-urls", "http://0.0.0.0:2379",
                "--advertise-client-urls", f"http://{etcd_container}:2379",
                "--listen-peer-urls", "http://127.0.0.1:2380",
                "--initial-advertise-peer-urls", "http://127.0.0.1:2380",
                "--initial-cluster", "disposable=http://127.0.0.1:2380", capture=True,
            )
            started_etcd = True
            for _ in range(40):
                ready = subprocess.run(
                    ["docker", "exec", etcd_container, "/usr/local/bin/etcdctl", "--endpoints=http://127.0.0.1:2379", "endpoint", "health"],
                    capture_output=True,
                )
                if ready.returncode == 0:
                    break
                time.sleep(0.25)
            else:
                raise RuntimeError("disposable etcd did not become healthy")
            state = json.loads(run("docker", "inspect", container, capture=True).stdout)[0]
            networks = state["NetworkSettings"]["Networks"]
            addresses = [networks[name]["IPAddress"] for name in (first_network, second_network)]
            if any(not address for address in addresses) or addresses[0] == addresses[1]:
                raise RuntimeError("disposable ingress networks did not assign distinct private IPs")
            print(f"Qualifying disposable ingress nodes {addresses[0]} and {addresses[1]}", flush=True)
            run(
                "docker", "exec", "-e", "NORN_TEST_INGRESS_PRIVATE_IPS=" + ",".join(addresses),
                "-e", "NORN_TEST_ETCD_ENDPOINTS=http://" + etcd_container + ":2379",
                container, "/work/etcdstore.test", "-test.run", "^TestV3CompletedFleetAttemptSelectsActiveIngressInventoryEtcd$", "-test.v",
            )
            run(
                "docker", "exec", "-e", "NORN_TEST_INGRESS_PRIVATE_IPS=" + ",".join(addresses),
                container, "/work/ingress.test", "-test.run", "^TestPrivateFleetInventoryPublisherTransport$", "-test.v",
            )
        finally:
            if started_etcd:
                subprocess.run(["docker", "rm", "-f", etcd_container], check=False, capture_output=True)
            if started_container:
                subprocess.run(["docker", "rm", "-f", container], check=False, capture_output=True)
            for network in reversed(created_networks):
                subprocess.run(["docker", "network", "rm", network], check=False, capture_output=True)


if __name__ == "__main__":
    main()
