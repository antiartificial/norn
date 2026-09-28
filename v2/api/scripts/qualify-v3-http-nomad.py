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
        env = os.environ.copy()
        env.update(
            NORN_TEST_ETCD_ENDPOINTS=f"http://127.0.0.1:{etcd_port}",
            NORN_TEST_NOMAD_ADDR=f"http://127.0.0.1:{nomad_port}",
            NORN_TEST_DEPLOYMENT_IMAGE=image,
        )
        print(f"Local etcd={etcd_port} Nomad={nomad_port}", flush=True)
        result = subprocess.run(
            ["go", "test", ".", "-run", "^TestEtcdFleetStagingReleaseHTTP(AcceptsAndReplaysVerifiedSource|ToDisposableNomad)$", "-count=1", "-v", "-timeout=120s"],
            cwd=repo / "v2/api", env=env, timeout=135,
        )
        exit_code = result.returncode
    finally:
        for process in (nomad, consul, etcd):
            process.terminate()
        for process in (nomad, consul, etcd):
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
