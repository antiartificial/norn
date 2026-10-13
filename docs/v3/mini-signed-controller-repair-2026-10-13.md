# Mini signed controller repair for the 2026-10-13 transition

This is a narrow recovery procedure for transition `mini-v3-20261013-3602e7e3`, source `3da50fe3a3c9f37d64e24c03a7dcfea94153da58`, target `3602e7e3d5fe5c0ef333b4041580d48ec15b24c5`, and the selected generation journal. It changes the controller code used to resume that target; it does not retarget the journal or active generation. Run it as the Mini owner only after the controller repair release is merged, published, and externally verified. Set `CONTROLLER_SHA` to that exact signed release's full SHA.

The old managed `platform-upgrade preflight` can reject the in-progress generation before fetching a new release. The following command imports the new release using the existing signed artifact fetch/import tools, independently verifies it, then runs the signed D-only repair, preservation, and finalization sequence. It reads the current audit key from the owner-only SOPS file into process memory and environment; it does not print the key or place it in command arguments. This generation's source evidence verifies with the current key; the historical key must not be supplied. Run on Mini as `0xadb`, with its normal SOPS and GitHub release access available. Do not run it from a shell with tracing enabled.

```sh
CONTROLLER_SHA=<full-40-character-published-repair-release-sha> python3 - <<'PY'
import json, os, re, stat, subprocess
from pathlib import Path

home = Path.home()
controller = os.environ["CONTROLLER_SHA"]
source = "3da50fe3a3c9f37d64e24c03a7dcfea94153da58"
candidate = "3602e7e3d5fe5c0ef333b4041580d48ec15b24c5"
transition = "mini-v3-20261013-3602e7e3"
generation = "gen-41bde512aa418c5977998d1b28ca7dd9"
if not re.fullmatch(r"[0-9a-f]{40}", controller):
    raise SystemExit("CONTROLLER_SHA must be an exact full SHA")

def private(path):
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != 0o600:
        raise SystemExit(f"unsafe owner-only configuration: {path}")
    return path

release_config = private(home / ".config/norn/release.env")
settings = {}
for line in release_config.read_text().splitlines():
    if not line or line.startswith("#"):
        continue
    if not re.fullmatch(r"[A-Z0-9_]+=[A-Za-z0-9_./:@+-]+", line):
        raise SystemExit("invalid release configuration")
    key, value = line.split("=", 1)
    if key in settings:
        raise SystemExit("duplicate release configuration key")
    settings[key] = value
if settings.get("NORN_RELEASE_REPOSITORY") != "antiartificial/norn":
    raise SystemExit("unexpected signed release repository")
public_key = settings["NORN_RELEASE_PUBLIC_KEY"]
releases = home / "norn/releases"
artifact = home / ".config/norn/host/bin/platform-release-artifact"
env = os.environ.copy()
env.update(settings)
if not (releases / controller).exists():
    bundle = home / "norn/.norn-release-cache" / controller / "darwin-arm64"
    if not bundle.exists():
        fetch = subprocess.check_output([
            str(artifact), "fetch", "--repository", "antiartificial/norn",
            "--commit", controller, "--tag", "platform-" + controller,
            "--cache-dir", str(home / "norn/.norn-release-cache"),
            "--public-key", public_key, "--os", "darwin", "--arch", "arm64",
        ], env=env, text=True).strip()
        if Path(fetch) != bundle:
            raise SystemExit("fetched bundle path differs from exact commit cache")
    subprocess.run([str(artifact), "import", "--bundle-dir", str(bundle),
                    "--releases-dir", str(releases), "--public-key", public_key,
                    "--quiet"], env=env, check=True)
subprocess.run([settings["NORN_RELEASE_VERIFY_HOOK"], str(releases / controller),
                str(releases / controller / "release.json")], env=env, check=True)

sops = "/opt/homebrew/bin/sops"
def decrypted(path):
    private(path)
    return json.loads(subprocess.check_output([sops, "--decrypt", "--output-type", "json", str(path)]))
active = decrypted(home / ".config/norn/api.env.enc.json")
if not active.get("NORN_AUDIT_SIGNING_KEY"):
    raise SystemExit("current audit key is unavailable")
env.update({str(key): str(value) for key, value in active.items()})
env.update(settings)
env.pop("NORN_AUDIT_HISTORICAL_SIGNING_KEY", None)
env["PATH"] = "/Applications/Postgres.app/Contents/Versions/latest/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
if "NORN_TOKEN" not in env and "NORN_API_BEARER_TOKEN" in env:
    env["NORN_TOKEN"] = env["NORN_API_BEARER_TOKEN"]
helper = releases / controller / "bin/mini-recovered-forward-controller-repair"
journal = home / ".config/norn/recovered-forward/generations" / generation / "transition.json"
os.execve(helper, [str(helper), "run", "--controller", str(releases / controller / "bin/platform-upgrade"),
                   "--source", source, "--candidate", candidate,
                   "--transition", transition, "--journal", str(journal)], env)
PY
```

The helper checks the signed controller/source/target releases, selected generation and evidence, phase-specific Mini runtime, and durable host fence. It writes a private HMAC authorization tied to the exact journal and current-release digests; the D controller rechecks it under the promotion lock. The helper then captures the unchanged candidate preservation receipt and invokes only D's finalizer. It stages the signed GUI API launcher under maintenance, uses the receipt-bound one-process normal activation, and restores the original API LaunchAgent after promotion. If interrupted, rerun this same command: each durable phase resumes only its validated next step. Keep the journal, signed plist record, and receipt intact if a verification fails; inspect the error before retrying.
