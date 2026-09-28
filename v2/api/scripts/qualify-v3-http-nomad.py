import os
import pathlib
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request


def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


repo = pathlib.Path(__file__).resolve().parents[3]
traefik_requested = os.environ.get("NORN_TEST_TRAEFIK_BINARY", "")
traefik_binary = pathlib.Path(traefik_requested)
if traefik_requested and not traefik_binary.is_file():
    raise SystemExit(f"Traefik rehearsal binary is missing: {traefik_binary}")
image = os.environ.get(
    "NORN_TEST_DEPLOYMENT_IMAGE",
    "docker.io/library/busybox@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662",
)
if subprocess.run(["docker", "image", "inspect", image], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode != 0:
    raise SystemExit(f"pull the exact image into Docker before qualification: {image}")
root = pathlib.Path(tempfile.mkdtemp(prefix="norn-v3-http-nomad-"))
try:
    etcd_port, peer, nomad_port, rpc, serf, consul_port, consul_dns, consul_server, consul_lan, consul_wan, consul_grpc, consul_grpc_tls = [port() for _ in range(12)]
    etcd = subprocess.Popen(
        [
            "etcd", "--name", "qual", "--data-dir", str(root / "etcd"),
            "--listen-client-urls", f"http://127.0.0.1:{etcd_port}",
            "--advertise-client-urls", f"http://127.0.0.1:{etcd_port}",
            "--listen-peer-urls", f"http://127.0.0.1:{peer}",
            "--initial-advertise-peer-urls", f"http://127.0.0.1:{peer}",
            "--initial-cluster", f"qual=http://127.0.0.1:{peer}",
            "--log-level", "error",
        ], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE,
    )
    consul = subprocess.Popen(
        [
            "consul", "agent", "-dev", "-client=127.0.0.1", "-bind=127.0.0.1",
            f"-http-port={consul_port}", f"-dns-port={consul_dns}",
            f"-server-port={consul_server}", f"-serf-lan-port={consul_lan}",
            f"-serf-wan-port={consul_wan}", f"-grpc-port={consul_grpc}",
            f"-grpc-tls-port={consul_grpc_tls}", "-log-level=err",
        ], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE,
    )
    config = root / "nomad.hcl"
    config.write_text(
        f'data_dir = "{root}/nomad"\n'
        f'bind_addr = "127.0.0.1"\n'
        f'ports {{ http = {nomad_port} rpc = {rpc} serf = {serf} }}\n'
        f'consul {{ address = "127.0.0.1:{consul_port}" }}\n'
    )
    nomad = subprocess.Popen(
        ["nomad", "agent", "-dev", "-config", str(config), "-log-level", "WARN"],
        stdout=subprocess.DEVNULL, stderr=subprocess.PIPE,
    )
    traefik_processes = []
    try:
        for name, url, process in [
            ("etcd", f"http://127.0.0.1:{etcd_port}/health", etcd),
            ("consul", f"http://127.0.0.1:{consul_port}/v1/status/leader", consul),
            ("nomad", f"http://127.0.0.1:{nomad_port}/v1/status/leader", nomad),
        ]:
            for _ in range(100):
                if process.poll() is not None:
                    raise RuntimeError(f"{name} exited: {process.stderr.read()[-1500:].decode()}")
                try:
                    with urllib.request.urlopen(url, timeout=0.3) as response:
                        if response.status == 200:
                            break
                except Exception:
                    time.sleep(0.2)
            else:
                raise RuntimeError(f"{name} not ready")
        if traefik_requested:
            certificate, key = root / "ingress-cert.pem", root / "ingress-key.pem"
            subprocess.run(
                ["openssl", "ecparam", "-name", "prime256v1", "-genkey", "-noout", "-out", str(key)],
                check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            )
            subprocess.run(
                ["openssl", "req", "-new", "-x509", "-key", str(key), "-out", str(certificate),
                 "-days", "1", "-subj", "/CN=*.example.test",
                 "-addext", "subjectAltName=DNS:*.example.test"],
                check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            )
            ingress_ports = []
            for index in range(2):
                web, secure, admin = port(), port(), port()
                directory = root / f"ingress-{index}"
                dynamic = directory / "dynamic"
                dynamic.mkdir(parents=True)
                (dynamic / "admin.yml").write_text(
                    "http:\n  routers:\n    local-admin:\n      rule: PathPrefix(`/api`)\n"
                    "      entryPoints: [admin]\n      service: api@internal\n"
                )
                (dynamic / "tls.yml").write_text(
                    f"tls:\n  stores:\n    default:\n      defaultCertificate:\n"
                    f"        certFile: {certificate}\n        keyFile: {key}\n"
                )
                traefik_config = directory / "traefik.yml"
                traefik_config.write_text(
                    f'entryPoints:\n  web:\n    address: "127.0.0.1:{web}"\n'
                    f'  websecure:\n    address: "127.0.0.1:{secure}"\n'
                    f'  admin:\n    address: "127.0.0.1:{admin}"\n'
                    f'api:\n  dashboard: false\nproviders:\n  file:\n'
                    f'    directory: {dynamic}\n    watch: true\n'
                    f'  consulCatalog:\n    endpoint:\n      address: "127.0.0.1:{consul_port}"\n'
                    f'    exposedByDefault: false\nlog:\n  level: ERROR\n'
                )
                log = (directory / "traefik.log").open("w")
                process = subprocess.Popen([str(traefik_binary), "--configfile", str(traefik_config)],
                                           stdout=log, stderr=subprocess.STDOUT)
                log.close()
                traefik_processes.append(process)
                ingress_ports.append((secure, admin, dynamic))
            for secure, admin, _ in ingress_ports:
                for _ in range(50):
                    try:
                        with urllib.request.urlopen(f"http://127.0.0.1:{admin}/api/rawdata", timeout=0.3) as response:
                            if response.status == 200:
                                break
                    except Exception:
                        time.sleep(0.2)
                else:
                    raise RuntimeError(f"Traefik at {admin} not ready")
        env = os.environ.copy()
        env.update(
            NORN_TEST_ETCD_ENDPOINTS=f"http://127.0.0.1:{etcd_port}",
            NORN_TEST_NOMAD_ADDR=f"http://127.0.0.1:{nomad_port}",
            NORN_TEST_DEPLOYMENT_IMAGE=image,
        )
        if traefik_processes:
            env.update(
                NORN_TEST_TRAEFIK_NODES=";".join(f"{secure},{admin},{dynamic}" for secure, admin, dynamic in ingress_ports),
                NORN_TEST_TRAEFIK_CA_FILE=str(certificate),
            )
            print("Two loopback Traefik ingress nodes ready", flush=True)
        print(f"Local etcd={etcd_port} Nomad={nomad_port}", flush=True)
        result = subprocess.run(
            ["go", "test", ".", "-run", "^TestEtcdFleetStagingReleaseHTTP(AcceptsAndReplaysVerifiedSource|ToDisposableNomad)$", "-count=1", "-v", "-timeout=120s"],
            cwd=repo / "v2/api", env=env, timeout=135,
        )
        exit_code = result.returncode
    finally:
        for process in (*traefik_processes, nomad, consul, etcd):
            process.terminate()
        for process in (*traefik_processes, nomad, consul, etcd):
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
finally:
    try:
        shutil.rmtree(root)
    except OSError:
        # Nomad's Docker driver can leave root-owned allocation log files.
        # Use an already-cached helper image on this exact disposable mount.
        helper = "busybox@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662"
        cached = subprocess.run(["docker", "image", "inspect", helper], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if cached.returncode == 0:
            subprocess.run(
                ["docker", "run", "--rm", "-v", f"{root}:/state", helper,
                 "chown", "-R", f"{os.getuid()}:{os.getgid()}", "/state"],
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False,
            )
        for attempt in range(10):
            try:
                shutil.rmtree(root)
                break
            except OSError as error:
                if attempt == 9:
                    print(f"Disposable state needs cleanup at {root}: {error}", file=sys.stderr)
                else:
                    time.sleep(0.5)
sys.exit(exit_code)
