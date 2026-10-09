#!/usr/bin/env python3
"""Verified, user-local Voxtype integration. Requires Python 3.11 or newer."""

import argparse
import contextlib
import fcntl
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import platform
import re
import shlex
import shutil
import socket
import stat
import subprocess
import sys
import tarfile
import tempfile
import time
import tomllib
import urllib.request

from voxtype_config import configure

VERSION = "v0.1.1"
RELEASE = f"https://github.com/DaDecky/codex-transcribe/releases/download/{VERSION}"
BACKEND = "codex-transcribe.service"
VOXTYPE = "voxtype.service"
MAX_ARCHIVE = 64 * 1024 * 1024
MAX_BINARY = 64 * 1024 * 1024


class InstallError(Exception):
    pass


def digest(data):
    return hashlib.sha256(data).hexdigest()


def run_command(args, *, env=None):
    try:
        return subprocess.run(args, capture_output=True, text=True, env=env, timeout=30)
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise InstallError(f"Cannot run {Path(args[0]).name}: {type(exc).__name__}") from exc


class Manager:
    def __init__(self, runner=run_command):
        self.runner = runner

    def call(self, *args, check=True):
        result = self.runner(["systemctl", "--user", *args])
        if check and result.returncode:
            raise InstallError(f"User service command failed: systemctl --user {' '.join(args)}; inspect your user journal locally")
        return result

    def state(self, unit):
        loaded = self.call("show", unit, "--property=LoadState", "--value", check=False).stdout.strip()
        if loaded not in {"loaded", "not-found"}:
            raise InstallError("Cannot inspect your user service manager; use --no-start when no user bus is available")
        if unit == VOXTYPE and loaded != "loaded":
            raise InstallError("voxtype.service must already exist; install your normal Voxtype user daemon first, or use --no-start")
        enabled = self.call("is-enabled", unit, check=False).stdout.strip()
        active = self.call("is-active", unit, check=False).stdout.strip()
        if enabled not in {"enabled", "enabled-runtime", "disabled", "static", "indirect", "not-found", ""}:
            raise InstallError(f"Unsupported enable state for {unit}: {enabled}; unmask or normalize it first")
        if active not in {"active", "inactive", "failed", "unknown", ""}:
            raise InstallError(f"{unit} is transitioning; wait until it is active or inactive")
        return {"enabled": enabled, "active": active == "active"}

    def text(self, unit):
        return self.call("cat", unit, check=False).stdout


def fetch(url, limit):
    if not url.startswith(RELEASE + "/"):
        raise InstallError("Only the pinned official HTTPS release is permitted")
    try:
        with urllib.request.urlopen(url, timeout=30) as response:
            if not response.geturl().startswith("https://"):
                raise InstallError("Release download redirected away from HTTPS")
            length = response.headers.get("Content-Length")
            if length and int(length) > limit:
                raise InstallError("Release download exceeds size limit")
            content = response.read(limit + 1)
    except (OSError, ValueError) as exc:
        raise InstallError("Official release download failed; check network access and try again manually") from exc
    if len(content) > limit:
        raise InstallError("Release download exceeds size limit")
    return content


def release_binary(downloader=fetch):
    if platform.system() != "Linux":
        raise InstallError("This installer supports Linux only")
    arch = {"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(platform.machine())
    if arch is None:
        raise InstallError("This installer supports Linux amd64 and arm64 only")
    asset = f"codex-transcribe_{VERSION}_linux_{arch}.tar.gz"
    checksum = downloader(f"{RELEASE}/{asset}.sha256", 4096)
    if len(checksum) > 4096:
        raise InstallError("Checksum download exceeds size limit")
    match = re.fullmatch(r"([0-9a-fA-F]{64})\s+\*?" + re.escape(asset) + r"\s*", checksum.decode("ascii"))
    if not match:
        raise InstallError("Official checksum file has an unexpected format")
    archive = downloader(f"{RELEASE}/{asset}", MAX_ARCHIVE)
    if len(archive) > MAX_ARCHIVE or digest(archive) != match.group(1).lower():
        raise InstallError("Release archive SHA256 verification failed; nothing was installed")
    binary = None
    expanded_size = 0
    try:
        with tarfile.open(fileobj=io.BytesIO(archive), mode="r:gz") as tar:
            for count, member in enumerate(tar):
                path = PurePosixPath(member.name)
                expanded_size += member.size
                if expanded_size > MAX_ARCHIVE * 2:
                    raise InstallError("Expanded release archive exceeds size limit")
                if count >= 4096 or path.is_absolute() or ".." in path.parts or member.issym() or member.islnk() or not (member.isfile() or member.isdir()):
                    raise InstallError("Unsafe release archive member")
                if member.size > MAX_BINARY:
                    raise InstallError("Release archive member exceeds size limit")
                if member.name == "codex-transcribe":
                    if binary is not None or not member.isfile():
                        raise InstallError("Release must contain exactly one regular codex-transcribe binary")
                    source = tar.extractfile(member)
                    binary = source.read(MAX_BINARY + 1)
                    if len(binary) != member.size or not binary:
                        raise InstallError("Truncated release binary")
    except (tarfile.TarError, EOFError) as exc:
        raise InstallError("Invalid release archive") from exc
    if binary is None:
        raise InstallError("Release archive has no codex-transcribe binary")
    return binary


def unit_quote(value):
    # systemd performs specifier/variable expansion even in double quotes.
    if any(c in value for c in "\x00\n\r"):
        raise InstallError("Paths containing line breaks or NUL are unsupported")
    return '"' + value.replace("\\", "\\\\").replace('"', '\\"').replace("%", "%%").replace("$", "$$") + '"'


def assignments(text, key):
    values = []
    section = None
    text = re.sub(r"\\\n\s*", " ", text)
    for line in text.splitlines():
        line = line.strip()
        if line.startswith("["):
            section = line
        elif section == "[Service]" and re.match(re.escape(key) + r"\s*=", line):
            value = line.split("=", 1)[1].strip()
            if not value:
                values.clear()
            else:
                values.append(value)
    return values


def check_voxtype(text, config, port):
    allowed = {"--engine": "whisper", "--whisper-mode": "remote",
               "--remote-endpoint": f"http://127.0.0.1:{port}",
               "--remote-model": "whisper-1", "--remote-api-key": "local-placeholder"}
    for command in assignments(text, "ExecStart"):
        args = shlex.split(command)
        for index, arg in enumerate(args):
            key, equal, value = arg.partition("=")
            if key.startswith("-c") and not key.startswith("--") and key != "-c":
                key, equal, value = "-c", "=", key[2:]
            if key in {"--eager-processing", "--eager", "--chunked"} and (not equal or value.lower() not in {"false", "0"}):
                raise InstallError("Remove eager/chunked processing from your Voxtype service first; launch flags are not changed by this installer")
            if key in allowed or key in {"--config", "-c"}:
                if not equal:
                    value = args[index + 1] if index + 1 < len(args) else ""
                expected = allowed.get(key, str(config))
                if key in {"--config", "-c"}:
                    value = value.replace("%h", str(Path.home()))
                if value != expected:
                    raise InstallError(f"Conflicting Voxtype launch override {key}; remove it or align it with this install before running again")
    if os.environ.get("VOXTYPE_WHISPER_API_KEY"):
        raise InstallError("Unset VOXTYPE_WHISPER_API_KEY before installation; it overrides the configured proxy key")
    for setting in assignments(text, "Environment"):
        if any(item.startswith("VOXTYPE_WHISPER_API_KEY=") and item.split("=", 1)[1] for item in shlex.split(setting)):
            raise InstallError("Remove VOXTYPE_WHISPER_API_KEY from the Voxtype service environment before installation")
    for setting in assignments(text, "EnvironmentFile"):
        for filename in shlex.split(setting):
            optional = filename.startswith("-")
            filename = filename.removeprefix("-").replace("%%", "\x00").replace("%h", str(Path.home()).replace("%", "\x00"))
            if "%" in filename or any(c in filename for c in "*?["):
                raise InstallError("Resolve Voxtype EnvironmentFile specifiers/globs before installing so transcription overrides can be checked")
            path = Path(filename.replace("\x00", "%"))
            if optional and not path.exists():
                continue
            environment_text = path.read_text()
            if re.search(r"^\s*VOXTYPE_WHISPER_API_KEY\s*=", environment_text, re.M):
                raise InstallError("Remove VOXTYPE_WHISPER_API_KEY from the Voxtype EnvironmentFile before installing")


def local_unit_text(unit_dir, unit):
    paths = [unit_dir / unit]
    paths.extend(sorted((unit_dir / (unit + ".d")).glob("*.conf")))
    return "\n".join(path.read_text() for path in paths if path.exists())


def own_listener(port, pid, binary):
    try:
        if pid <= 0 or Path(f"/proc/{pid}/exe").resolve() != binary.resolve():
            return False
        sockets = {os.readlink(fd)[8:-1] for fd in Path(f"/proc/{pid}/fd").iterdir()
                   if os.readlink(fd).startswith("socket:[")}
        listeners = []
        for name in ("tcp", "tcp6"):
            for line in Path(f"/proc/net/{name}").read_text().splitlines()[1:]:
                fields = line.split()
                if fields[3] == "0A" and int(fields[1].split(":")[1], 16) == port:
                    listeners.append(fields[9])
        return bool(listeners) and all(inode in sockets for inode in listeners)
    except (OSError, ValueError):
        return False


def backend_ready(port, binary, manager):
    # Wait only for the local process to bind, then issue one bounded health
    # request. Downloads and HTTP requests are never retried. No audio/auth is sent.
    deadline = time.monotonic() + 5
    while True:
        if not manager.state(BACKEND)["active"]:
            raise InstallError("Backend exited before becoming ready")
        try:
            pid = int(manager.call("show", BACKEND, "--property=MainPID", "--value").stdout.strip())
        except ValueError:
            pid = 0
        if own_listener(port, pid, binary):
            break
        if time.monotonic() >= deadline:
            raise InstallError("Backend did not own its loopback listener within five seconds")
        time.sleep(0.05)
    try:
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        with opener.open(f"http://127.0.0.1:{port}/healthz", timeout=2) as response:
            content = response.read(4097)
            healthy = response.status == 200 and len(content) <= 4096 and json.loads(content) == {"status": "ok"}
    except (OSError, ValueError) as exc:
        raise InstallError("Backend health check failed; prior installation will be restored") from exc
    if not healthy or not manager.state(BACKEND)["active"] or not own_listener(port, pid, binary):
        raise InstallError("Backend failed its owned-process health check")


def probe_port(port):
    with socket.socket() as sock:
        # Match the Go listener: closed connections in TIME_WAIT are not owners.
        sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        try:
            sock.bind(("127.0.0.1", port))
        except OSError:
            return False
    return True


def atomic_write(path, content, mode):
    fd, temporary = tempfile.mkstemp(prefix=".codex-transcribe-", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as file:
            file.write(content)
            file.flush()
            os.fchmod(file.fileno(), mode)
            os.fsync(file.fileno())
        os.replace(temporary, path)
    finally:
        with contextlib.suppress(FileNotFoundError):
            os.unlink(temporary)


def save_manifest(backup, manifest):
    atomic_write(backup / "manifest.json", json.dumps(manifest, indent=2).encode() + b"\n", 0o600)


def file_hash(path):
    if path.is_symlink() or (path.exists() and not path.is_file()):
        raise InstallError(f"Refusing non-regular target: {path}")
    return digest(path.read_bytes()) if path.exists() else None


def restore_services(manager, states):
    manager.call("daemon-reload")
    original = states[BACKEND]
    if original["enabled"] in {"enabled", "enabled-runtime"}:
        args = ["enable"]
        if original["enabled"] == "enabled-runtime":
            args.append("--runtime")
        manager.call(*args, BACKEND)
    elif original["enabled"] not in {"not-found", ""}:
        manager.call("disable", BACKEND)
    if original["active"]:
        manager.call("restart", BACKEND)
    elif original["enabled"] not in {"not-found", ""}:
        manager.call("stop", BACKEND)
    manager.call("restart" if states[VOXTYPE]["active"] else "stop", VOXTYPE)


def restore(backup, manifest, manager, *, failed=False):
    # Check every file before changing any file or stopping a service.
    entries = [entry for entry in manifest["files"] if entry["touched"]]
    for entry in entries:
        actual = file_hash(Path(entry["path"]))
        acceptable = {entry["installed_hash"]}
        if failed:
            acceptable.add(entry["original_hash"])
        if actual not in acceptable:
            raise InstallError(f"Rollback refused: {entry['path']} changed since install. Preserve/reconcile your edits with {backup} before retrying")
    for entry in entries:
        if entry["original_hash"] is not None:
            old = backup / entry["backup"]
            if digest(old.read_bytes()) != entry["original_hash"]:
                raise InstallError("Rollback backup is damaged; restore it before proceeding")
    manifest["status"] = "rolling-back"
    save_manifest(backup, manifest)
    if manifest["manager_mutated"]:
        manager.call("stop", BACKEND, check=manifest["services"][BACKEND]["enabled"] not in {"not-found", ""})
        # Disable while our unit still exists, removing enable links added at install.
        manager.call("disable", BACKEND, check=manifest["services"][BACKEND]["enabled"] not in {"not-found", ""})
    for entry in reversed(entries):
        target = Path(entry["path"])
        if entry["original_hash"] is None:
            target.unlink(missing_ok=True)
        else:
            atomic_write(target, (backup / entry["backup"]).read_bytes(), entry["original_mode"])
    if manifest["manager_mutated"]:
        restore_services(manager, manifest["services"])
    for directory in reversed(manifest["created_dirs"]):
        with contextlib.suppress(OSError):
            Path(directory).rmdir()
    manifest["status"] = "failed-restored" if failed else "rolled-back"
    save_manifest(backup, manifest)


def rollback(backup, manager):
    backup = Path(backup).absolute()
    if backup.is_symlink() or not backup.is_dir() or backup.stat().st_uid != os.getuid() or stat.S_IMODE(backup.stat().st_mode) & 0o077:
        raise InstallError("Rollback requires your own private installer backup directory")
    manifest = json.loads((backup / "manifest.json").read_text())
    if manifest.get("format") != 1 or manifest.get("status") not in {"installed", "installing", "recovery-needed", "rolling-back"}:
        raise InstallError("Backup is not an outstanding installer transaction; completed rollbacks cannot be repeated")
    restore(backup, manifest, manager, failed=manifest["status"] != "installed")
    return backup


def ensure_parent(path, manifest, backup):
    missing = []
    current = path.parent
    while not current.exists():
        missing.append(current)
        current = current.parent
    for directory in reversed(missing):
        manifest["created_dirs"].append(str(directory))
        save_manifest(backup, manifest)
        directory.mkdir(mode=0o700)


def install(port, no_start, state_root, *, downloader=fetch, runner=run_command, manager=None, readiness=backend_ready):
    manager = manager or Manager(runner)
    home = Path.home()
    config_root = Path(os.environ.get("XDG_CONFIG_HOME", home / ".config")).absolute()
    config = config_root / "voxtype/config.toml"
    unit_dir = config_root / "systemd/user"
    binary_path = home / ".local/bin/codex-transcribe"
    unit_path = unit_dir / BACKEND
    dropin = unit_dir / "voxtype.service.d/10-codex-transcribe.conf"
    if not shutil.which("voxtype"):
        raise InstallError("Voxtype must already be installed and available in PATH")
    if file_hash(config) is None:
        raise InstallError(f"Existing Voxtype config required: {config}")
    text = config.read_text()
    try:
        parsed = tomllib.loads(text)
    except tomllib.TOMLDecodeError as exc:
        raise InstallError(f"Fix malformed TOML in {config} before installation; no settings were changed") from exc
    whisper = parsed.get("whisper", {})
    if not isinstance(whisper, dict):
        raise InstallError("Voxtype whisper configuration must be a TOML table")
    if whisper.get("eager_processing") or parsed.get("eager_processing"):
        raise InstallError("Disable eager_processing in your Voxtype config first; buffered remote transcription must not be chunked")
    endpoint = f"http://127.0.0.1:{port}"
    try:
        new_config = configure(text, endpoint).encode()
        tomllib.loads(new_config.decode())
    except ValueError as exc:
        raise InstallError(f"Cannot safely update TOML in {config}; normalize its whisper table without removing your existing preferences, then retry") from exc
    states = None
    if no_start:
        voxtype_text = local_unit_text(unit_dir, VOXTYPE)
        backend_text = local_unit_text(unit_dir, BACKEND)
    else:
        if not shutil.which("systemctl"):
            raise InstallError("systemctl is required; use --no-start for an offline install")
        states = {unit: manager.state(unit) for unit in (BACKEND, VOXTYPE)}
        voxtype_text = manager.text(VOXTYPE)
        backend_text = manager.text(BACKEND)
        environment = manager.call("show", VOXTYPE, "--property=Environment", "--value").stdout
        voxtype_text += "\n[Service]\nEnvironment=" + environment
        global_environment = manager.call("show-environment").stdout
        if re.search(r"^VOXTYPE_WHISPER_API_KEY=.+", global_environment, re.M):
            raise InstallError("Unset VOXTYPE_WHISPER_API_KEY in your user manager before installing; the installer does not alter global environment")
        if manager.call("show", BACKEND, "--property=DropInPaths", "--value").stdout.strip():
            raise InstallError("Existing backend drop-ins must be reviewed and removed manually before this installer can own codex-transcribe.service")
    check_voxtype(voxtype_text, config, port)
    commands = assignments(backend_text, "ExecStart")
    if unit_path.exists() or backend_text.strip():
        if len(commands) != 1 or Path(shlex.split(commands[0])[0]).name != "codex-transcribe":
            raise InstallError("Existing codex-transcribe.service is not a recognized same-product unit; move it aside manually first")
    # Backend drop-ins can defeat our explicit listen/auth/environment settings.
    if list((unit_dir / (BACKEND + ".d")).glob("*.conf")):
        raise InstallError("Existing backend drop-ins must be reviewed and removed manually before this installer can own codex-transcribe.service")
    occupied = not probe_port(port)
    if occupied:
        pid = 0
        if not no_start and states[BACKEND]["active"]:
            try:
                pid = int(manager.call("show", BACKEND, "--property=MainPID", "--value").stdout.strip())
            except ValueError:
                pass
        if not own_listener(port, pid, binary_path):
            raise InstallError(f"127.0.0.1:{port} is occupied by another process; choose --port or stop it yourself")
    auth = Path(os.environ.get("CODEX_TRANSCRIBE_AUTH_FILE") or
                str(Path(os.environ.get("CODEX_HOME", home / ".codex")) / "auth.json")).absolute()
    binary = release_binary(downloader)
    with tempfile.TemporaryDirectory(prefix="codex-transcribe-preflight-") as temporary:
        candidate = Path(temporary) / "codex-transcribe"
        candidate.write_bytes(binary)
        candidate.chmod(0o700)
        result = runner([str(candidate), "-version"])
        if result.returncode or result.stdout.strip() != "codex-transcribe " + VERSION:
            raise InstallError("Verified binary did not report the pinned version")
        doctor_port = port
        if occupied:
            with socket.socket() as probe:
                probe.bind(("127.0.0.1", 0))
                doctor_port = probe.getsockname()[1]
        environment = dict(os.environ)
        environment.pop("CODEX_TRANSCRIBE_API_KEY", None)
        result = runner([str(candidate), "doctor", "-listen", f"127.0.0.1:{doctor_port}", "-auth-file", str(auth)], env=environment)
        if result.returncode:
            raise InstallError("Offline doctor failed before installation; run codex login and check the auth-file and selected port. No account validity was tested")
    unit = ("[Unit]\nDescription=codex-transcribe backend for Voxtype\n"
            "Documentation=https://github.com/DaDecky/codex-transcribe\n\n[Service]\nType=simple\n"
            f"ExecStart={unit_quote(str(binary_path))} -listen 127.0.0.1:{port} -auth-file {unit_quote(str(auth))}\n"
            "UnsetEnvironment=CODEX_TRANSCRIBE_API_KEY\nRestart=on-failure\nRestartSec=3\n"
            "TimeoutStopSec=15\n\n[Install]\nWantedBy=default.target\n").encode()
    dependency = b"# Managed by install-voxtype.py; no Voxtype launch flags are changed.\n[Unit]\nRequires=codex-transcribe.service\nAfter=codex-transcribe.service\n"
    targets = [(binary_path, binary, 0o755), (config, new_config, 0o600),
               (unit_path, unit, 0o644), (dropin, dependency, 0o644)]
    # Refuse symlinks/non-files and collect everything before writing any target.
    originals = [(path, file_hash(path), path.read_bytes() if path.exists() else None,
                  stat.S_IMODE(path.stat().st_mode) if path.exists() else None)
                 for path, _, _ in targets]
    backup = Path(tempfile.mkdtemp(prefix="install-", dir=state_root))
    manifest = {"format": 1, "status": "installing", "port": port,
                "services": states, "manager_mutated": False, "created_dirs": [], "files": []}
    for index, ((path, content, mode), (_, old_hash, old, old_mode)) in enumerate(zip(targets, originals)):
        name = f"{index}.before"
        if old is not None:
            atomic_write(backup / name, old, 0o600)
        manifest["files"].append({"path": str(path), "backup": name,
                                  "original_hash": old_hash, "original_mode": old_mode,
                                  "installed_hash": digest(content), "touched": False})
    save_manifest(backup, manifest)
    try:
        if not no_start:
            manifest["manager_mutated"] = True
            save_manifest(backup, manifest)
            if states[BACKEND]["active"]:
                manager.call("stop", BACKEND)
        for (path, content, mode), entry in zip(targets, manifest["files"]):
            if file_hash(path) != entry["original_hash"]:
                raise InstallError(f"Target changed during preflight: {path}; retry after other edits finish")
            ensure_parent(path, manifest, backup)
            entry["touched"] = True
            save_manifest(backup, manifest)
            atomic_write(path, content, mode)
        if not no_start:
            manager.call("daemon-reload")
            manager.call("enable", BACKEND)
            manager.call("restart", BACKEND)
            readiness(port, binary_path, manager)
            if states[VOXTYPE]["active"]:
                manager.call("restart", VOXTYPE)
                if not manager.state(VOXTYPE)["active"]:
                    raise InstallError("Voxtype did not remain active after restart")
        manifest["status"] = "installed"
        save_manifest(backup, manifest)
    except BaseException as exc:
        try:
            restore(backup, manifest, manager, failed=True)
        except BaseException as recovery:
            manifest["status"] = "recovery-needed"
            save_manifest(backup, manifest)
            raise InstallError(f"Install failed and recovery needs attention. Backup: {backup}. {recovery}") from exc
        raise InstallError(f"Install failed; prior files and service states restored. Backup retained: {backup}") from exc
    return backup


@contextlib.contextmanager
def installer_lock(state_root):
    state_root.mkdir(parents=True, exist_ok=True, mode=0o700)
    if state_root.is_symlink() or state_root.stat().st_uid != os.getuid() or stat.S_IMODE(state_root.stat().st_mode) & 0o077:
        raise InstallError(f"Installer state directory must be owned by you and private (chmod 700): {state_root}")
    fd = os.open(state_root / ".installer.lock", os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    try:
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as exc:
            raise InstallError("Another Voxtype installation/rollback is running") from exc
        yield
    finally:
        os.close(fd)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command")
    installer = subparsers.add_parser("install", help="install verified backend and configure existing Voxtype")
    installer.add_argument("--port", type=int, default=8378)
    installer.add_argument("--no-start", action="store_true", help="write files only; do not contact/change the user service manager")
    undo = subparsers.add_parser("rollback", help="restore an outstanding private backup transaction")
    undo.add_argument("backup_dir", type=Path)
    args = list(sys.argv[1:] if argv is None else argv)
    if not args or args[0].startswith("-") and args[0] not in {"-h", "--help"}:
        args.insert(0, "install")
    args = parser.parse_args(args)
    state_root = Path(os.environ.get("XDG_STATE_HOME", Path.home() / ".local/state")) / "codex-transcribe/voxtype-installs"
    try:
        with installer_lock(state_root.absolute()):
            if args.command == "rollback":
                backup = rollback(args.backup_dir, Manager())
                print(f"Rollback complete; backup retained: {backup}")
            else:
                if not 1 <= args.port <= 65535:
                    raise InstallError("--port must be between 1 and 65535")
                backup = install(args.port, args.no_start, state_root.absolute())
                print(f"Voxtype configured for http://127.0.0.1:{args.port}; backup: {backup}")
                print("Offline checks do not establish account validity. No audio was recorded or uploaded.")
                if args.no_start:
                    print("--no-start: no services changed. Reload/enable/start codex-transcribe.service and restart your Voxtype daemon manually when ready.")
        return 0
    except (InstallError, OSError, ValueError, UnicodeError, KeyError) as exc:
        # Never relay subprocess output, config/credential parsing detail, or tokens.
        message = str(exc) if isinstance(exc, InstallError) else f"{type(exc).__name__}; check local paths/configuration and permissions"
        print("Error: " + message, file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())