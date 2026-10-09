#!/usr/bin/env python3
"""Local-only transaction tests; no network outside a loopback fixture/account."""

import hashlib
import http.server
import importlib.util
import io
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tarfile
import tempfile
import threading
import tomllib
import unittest
from unittest.mock import patch
import urllib.request

sys.path.insert(0, str(Path(__file__).parent))
spec = importlib.util.spec_from_file_location("install_voxtype", Path(__file__).with_name("install-voxtype.py"))
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)

# A real executable fixture: version, credential validation and bind/release are
# exercised by subprocesses, rather than mocking successful doctor responses.
FAKE_BINARY = (f"#!{sys.executable}\n" + '''import json, os, socket, sys
args = sys.argv[1:]
if args == ['-version']:
    print('codex-transcribe v0.1.1')
    sys.exit(0)
if not args or args[0] != 'doctor':
    sys.exit(8)
try:
    path = args[args.index('-auth-file') + 1]
    auth = json.load(open(path))
    if not auth['tokens']['access_token']:
        sys.exit(2)
    if os.environ.get('CODEX_TRANSCRIBE_API_KEY'):
        sys.exit(3)
    listen = args[args.index('-listen') + 1]
    host, port = listen.rsplit(':', 1)
    if host != '127.0.0.1':
        sys.exit(4)
    with socket.socket() as probe:
        probe.bind((host, int(port)))
except Exception:
    sys.exit(5)
''').encode()


def make_archive(members=None):
    stream = io.BytesIO()
    with tarfile.open(fileobj=stream, mode="w:gz") as archive:
        for name, content, kind in members or [("codex-transcribe", FAKE_BINARY, tarfile.REGTYPE)]:
            member = tarfile.TarInfo(name)
            member.type = kind
            member.mode = 0o755
            if kind == tarfile.REGTYPE:
                member.size = len(content)
                archive.addfile(member, io.BytesIO(content))
            else:
                member.linkname = content.decode()
                archive.addfile(member)
    return stream.getvalue()


class ReleaseFixture:
    def __init__(self):
        self.archive = make_archive()
        self.bad_checksum = False
        self.requests = []

    def __enter__(self):
        fixture = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                fixture.requests.append(self.path)
                asset = self.path.split("/")[-1]
                if asset.endswith(".sha256"):
                    filename = asset.removesuffix(".sha256")
                    checksum = "0" * 64 if fixture.bad_checksum else hashlib.sha256(fixture.archive).hexdigest()
                    data = f"{checksum}  {filename}\n".encode()
                else:
                    data = fixture.archive
                self.send_response(200)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def log_message(self, *args):
                pass

        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        return self

    def __exit__(self, *args):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()

    def download(self, official_url, cap):
        # Dependency injection exists only in this Python test; production has
        # no arbitrary URL override. Keep official naming/checksum behavior.
        if not official_url.startswith(installer.RELEASE + "/"):
            raise AssertionError("wrong official release")
        url = f"http://127.0.0.1:{self.server.server_port}/" + official_url.rsplit("/", 1)[-1]
        with urllib.request.urlopen(url) as response:
            content = response.read(cap + 1)
        if len(content) > cap:
            raise installer.InstallError("Fixture download exceeds cap")
        return content


class FakeManager:
    """Stateful user-manager boundary, including a one-shot mutation failure."""
    def __init__(self, unit_dir, enabled="disabled", backend_active=False, voxtype_active=True, fail=None):
        self.unit_dir = unit_dir
        self.states = {
            installer.BACKEND: {"enabled": enabled, "active": backend_active},
            installer.VOXTYPE: {"enabled": "enabled", "active": voxtype_active},
        }
        self.fail = fail
        self.calls = []

    def state(self, unit):
        return dict(self.states[unit])

    def text(self, unit):
        return installer.local_unit_text(self.unit_dir, unit)

    def call(self, *args, check=True):
        self.calls.append(args)
        if args == self.fail:
            self.fail = None
            raise installer.InstallError("Injected service mutation failure")
        action = args[0]
        if action == "enable":
            self.states[args[-1]]["enabled"] = "enabled-runtime" if "--runtime" in args else "enabled"
        elif action == "disable":
            self.states[args[-1]]["enabled"] = "disabled"
        elif action in {"restart", "stop"}:
            self.states[args[-1]]["active"] = action == "restart"
        elif action == "daemon-reload" and not (self.unit_dir / installer.BACKEND).exists():
            self.states[installer.BACKEND] = {"enabled": "not-found", "active": False}
        return subprocess.CompletedProcess(args, 0, stdout="", stderr="")


class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="voxtype tests % $ ")
        self.addCleanup(self.temporary.cleanup)
        self.home = Path(self.temporary.name)
        self.config_root = self.home / "config with spaces"
        self.state_root = self.home / "state/voxtype-installs"
        self.config = self.config_root / "voxtype/config.toml"
        self.config.parent.mkdir(parents=True)
        self.original_config = b'''# Do not lose output preferences\nengine = "local"\n[whisper]\nmodel = "large-v3-turbo"\nlanguage = "en"\n[output]\nmode = "paste"\nauto_submit = false\npost_process = "my-existing-paste-hook"\n'''
        self.config.write_bytes(self.original_config)
        self.config.chmod(0o640)
        self.unit_dir = self.config_root / "systemd/user"
        self.unit_dir.mkdir(parents=True)
        self.voxtype_unit = self.unit_dir / installer.VOXTYPE
        self.voxtype_unit.write_text("[Service]\nExecStart=/usr/bin/voxtype --paste daemon\n[Install]\nWantedBy=default.target\n")
        self.unit = self.unit_dir / installer.BACKEND
        self.binary = self.home / ".local/bin/codex-transcribe"
        self.dropin = self.unit_dir / "voxtype.service.d/10-codex-transcribe.conf"
        self.tools = self.home / "tools"
        self.tools.mkdir()
        (self.tools / "voxtype").write_text("#!/bin/sh\nexit 0\n")
        (self.tools / "voxtype").chmod(0o755)
        (self.tools / "systemctl").write_text("#!/bin/sh\nexit 1\n")
        (self.tools / "systemctl").chmod(0o755)
        auth = self.home / ".codex/auth.json"
        auth.parent.mkdir()
        auth.write_text(json.dumps({"tokens": {"access_token": "local-fixture-never-an-account"}}))
        self.environment = patch.dict(os.environ, {
            "HOME": str(self.home), "XDG_CONFIG_HOME": str(self.config_root),
            "XDG_STATE_HOME": str(self.home / "state"), "PATH": str(self.tools) + os.pathsep + os.environ.get("PATH", ""),
            "CODEX_HOME": str(auth.parent), "CODEX_TRANSCRIBE_AUTH_FILE": str(auth),
            "CODEX_TRANSCRIBE_API_KEY": "must-be-unset-for-backend",
            "VOXTYPE_WHISPER_API_KEY": "",
        })
        self.environment.start()
        self.addCleanup(self.environment.stop)
        self.release = ReleaseFixture().__enter__()
        self.addCleanup(self.release.__exit__, None, None, None)
        with socket.socket() as probe:
            probe.bind(("127.0.0.1", 0))
            self.port = probe.getsockname()[1]

    def install(self, no_start=True, manager=None, runner=installer.run_command, readiness=None):
        if readiness is None:
            # Stateful manager fixtures do not launch actual units. The real
            # ownership/HTTP readiness implementation is exercised separately.
            readiness = lambda port, binary, manager: None
        with installer.installer_lock(self.state_root):
            return installer.install(self.port, no_start, self.state_root,
                                     downloader=self.release.download, runner=runner,
                                     manager=manager, readiness=readiness)

    def old_backend(self):
        self.binary.parent.mkdir(parents=True, exist_ok=True)
        self.binary.write_bytes(b"previous executable")
        self.binary.chmod(0o711)
        self.unit.write_text("[Service]\nExecStart=%h/.local/bin/codex-transcribe -listen 127.0.0.1:8377\n[Install]\nWantedBy=graphical-session.target\n")
        self.dropin.parent.mkdir(exist_ok=True)
        self.dropin.write_bytes(b"[Unit]\nAfter=existing.service\n")
        other = self.dropin.parent / "20-local-launch.conf"
        other.write_text("# Keep user's launch behavior\n[Service]\nExecStart=\nExecStart=/usr/bin/voxtype daemon\n")
        return {path: (path.read_bytes(), path.stat().st_mode & 0o777) for path in
                (self.binary, self.config, self.unit, self.dropin, other, self.voxtype_unit)}

    def assert_originals(self, originals):
        for path, (content, mode) in originals.items():
            self.assertEqual(path.read_bytes(), content, path)
            self.assertEqual(path.stat().st_mode & 0o777, mode, path)

    def test_no_start_install_and_cli_rollback_without_bus(self):
        self.voxtype_unit.unlink()  # No daemon unit or user bus required offline.
        backup = self.install()
        config = tomllib.loads(self.config.read_text())
        self.assertEqual(config["whisper"]["remote_endpoint"], f"http://127.0.0.1:{self.port}")
        self.assertEqual(config["whisper"]["language"], "en")
        self.assertEqual(config["output"]["post_process"], "my-existing-paste-hook")
        self.assertEqual(self.binary.read_bytes(), FAKE_BINARY)
        self.assertIn("%%", self.unit.read_text())
        self.assertIn("$$", self.unit.read_text())
        self.assertIn('"', self.unit.read_text())
        self.assertEqual(backup.stat().st_mode & 0o777, 0o700)
        self.assertEqual((backup / "manifest.json").stat().st_mode & 0o777, 0o600)
        result = subprocess.run([sys.executable, str(Path(installer.__file__)), "rollback", str(backup)], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.config.read_bytes(), self.original_config)
        self.assertEqual(self.config.stat().st_mode & 0o777, 0o640)
        for path in (self.binary, self.unit, self.dropin):
            self.assertFalse(path.exists(), path)
        self.assertEqual(json.loads((backup / "manifest.json").read_text())["status"], "rolled-back")
        with self.assertRaises(installer.InstallError):
            installer.rollback(backup, FakeManager(self.unit_dir))

    def test_existing_files_and_other_dropins_restored(self):
        originals = self.old_backend()
        backup = self.install()
        installer.rollback(backup, FakeManager(self.unit_dir))
        self.assert_originals(originals)

    def test_rerun_rollbacks_restore_each_predecessor(self):
        first = self.install()
        first_config = self.config.read_bytes()
        second = self.install()
        installer.rollback(second, FakeManager(self.unit_dir))
        self.assertEqual(self.config.read_bytes(), first_config)
        installer.rollback(first, FakeManager(self.unit_dir))
        self.assertEqual(self.config.read_bytes(), self.original_config)
        self.assertFalse(self.binary.exists())

    def test_edited_target_refuses_all_rollback_mutation(self):
        backup = self.install()
        self.config.write_bytes(self.config.read_bytes() + b"\n# User edit after installation\n")
        before = {path: path.read_bytes() for path in (self.binary, self.config, self.unit, self.dropin)}
        manager = FakeManager(self.unit_dir)
        with self.assertRaisesRegex(installer.InstallError, "changed since install"):
            installer.rollback(backup, manager)
        self.assertFalse(manager.calls)
        for path, content in before.items():
            self.assertEqual(path.read_bytes(), content)

    def test_service_failure_restores_files_and_both_states(self):
        for enabled in ("enabled", "enabled-runtime", "disabled"):
            for voxtype_active in (False, True):
                with self.subTest(enabled=enabled, voxtype_active=voxtype_active):
                    originals = self.old_backend()
                    manager = FakeManager(self.unit_dir, enabled, True, voxtype_active,
                                          fail=("restart", installer.BACKEND))
                    before = {unit: dict(state) for unit, state in manager.states.items()}
                    with self.assertRaisesRegex(installer.InstallError, "prior files and service states restored"):
                        self.install(no_start=False, manager=manager)
                    self.assert_originals(originals)
                    self.assertEqual(manager.states, before)
                    if not voxtype_active:
                        self.assertNotIn(("restart", installer.VOXTYPE), manager.calls)

    def test_default_install_preserves_inactive_voxtype_and_restores_new_backend(self):
        manager = FakeManager(self.unit_dir, "not-found", False, False)
        backup = self.install(no_start=False, manager=manager)
        self.assertTrue(manager.states[installer.BACKEND]["active"])
        self.assertEqual(manager.states[installer.BACKEND]["enabled"], "enabled")
        self.assertFalse(manager.states[installer.VOXTYPE]["active"])
        installer.rollback(backup, manager)
        self.assertFalse(manager.states[installer.BACKEND]["active"])
        self.assertFalse(manager.states[installer.VOXTYPE]["active"])
        self.assertFalse(self.unit.exists())
        self.assertEqual(manager.states[installer.BACKEND]["enabled"], "not-found")

    def test_invalid_credentials_fail_before_any_target_mutation(self):
        Path(os.environ["CODEX_TRANSCRIBE_AUTH_FILE"]).write_text('{"tokens":{"access_token":""}}')
        with self.assertRaisesRegex(installer.InstallError, "Offline doctor failed"):
            self.install()
        self.assertEqual(self.config.read_bytes(), self.original_config)
        self.assertFalse(self.binary.exists())
        self.assertFalse(list(self.state_root.glob("install-*")))

    def test_checksum_and_unsafe_archive_fail_before_mutation(self):
        self.release.bad_checksum = True
        with self.assertRaisesRegex(installer.InstallError, "SHA256"):
            self.install()
        self.release.bad_checksum = False
        for members in ([('../codex-transcribe', FAKE_BINARY, tarfile.REGTYPE)],
                        [('codex-transcribe', b'/tmp/evil', tarfile.SYMTYPE)],
                        [('codex-transcribe', FAKE_BINARY, tarfile.REGTYPE)] * 2):
            with self.subTest(members=members):
                self.release.archive = make_archive(members)
                with self.assertRaises(installer.InstallError):
                    self.install()
                self.assertEqual(self.config.read_bytes(), self.original_config)
                self.assertFalse(self.binary.exists())

    def test_occupied_unrelated_port_is_not_stopped(self):
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", self.port))
            listener.listen()
            manager = FakeManager(self.unit_dir)
            with self.assertRaisesRegex(installer.InstallError, "occupied"):
                self.install(manager=manager)
            self.assertFalse(manager.calls)
            self.assertFalse(self.release.requests)
            self.assertEqual(self.config.read_bytes(), self.original_config)

    def test_conflicting_voxtype_flags_fail_before_download(self):
        for argument in ("--engine parakeet", "--eager-processing", "--whisper-mode local",
                         "--remote-endpoint http://127.0.0.1:1111", "--remote-model other",
                         "--remote-api-key private", "--config /different/config.toml", "-c /different/config.toml"):
            with self.subTest(argument=argument):
                self.voxtype_unit.write_text(f"[Service]\nExecStart=/usr/bin/voxtype {argument} daemon\n")
                with self.assertRaises(installer.InstallError):
                    self.install()
                self.assertFalse(self.release.requests)
                self.assertEqual(self.config.read_bytes(), self.original_config)

    def test_symlink_targets_and_concurrent_transactions_refused(self):
        target = self.home / "unrelated"
        target.write_bytes(b"never replace")
        self.binary.parent.mkdir(parents=True)
        self.binary.symlink_to(target)
        with self.assertRaisesRegex(installer.InstallError, "non-regular"):
            self.install()
        self.assertEqual(target.read_bytes(), b"never replace")
        with installer.installer_lock(self.state_root):
            with self.assertRaisesRegex(installer.InstallError, "Another"):
                with installer.installer_lock(self.state_root):
                    self.fail("second lock acquired")

    def test_file_write_failure_restores_already_changed_files(self):
        originals = self.old_backend()
        actual_write = installer.atomic_write
        failed = False

        def write_once_failure(path, content, mode):
            nonlocal failed
            if path == self.unit and not failed:
                failed = True
                raise OSError("injected filesystem failure")
            actual_write(path, content, mode)

        with patch.object(installer, "atomic_write", side_effect=write_once_failure):
            with self.assertRaisesRegex(installer.InstallError, "prior files and service states restored"):
                self.install()
        self.assert_originals(originals)

    def test_real_process_listener_ownership_proof(self):
        # A copied native Python executable provides a real /proc exe and socket
        # owner, without compiling or invoking any production/user daemon.
        self.binary.parent.mkdir(parents=True)
        self.binary.write_bytes(Path(sys.executable).resolve().read_bytes())
        self.binary.chmod(0o700)
        program = '''import http.server
class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200 if self.path == '/healthz' else 404)
        self.end_headers()
        self.wfile.write(b'{"status":"ok"}\\n')
    def log_message(self, *args):
        pass
''' + f"server=http.server.HTTPServer(('127.0.0.1',{self.port}),Handler)\nprint('ready',flush=True)\nserver.serve_forever()\n"
        child = subprocess.Popen([str(self.binary), "-u", "-c", program], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        try:
            self.assertEqual(child.stdout.readline().strip(), "ready")
            self.assertTrue(installer.own_listener(self.port, child.pid, self.binary))
            self.assertFalse(installer.own_listener(self.port, os.getpid(), self.binary))
            self.assertFalse(installer.probe_port(self.port))
            class RunningManager(FakeManager):
                def call(inner, *args, check=True):
                    if args == ("show", installer.BACKEND, "--property=MainPID", "--value"):
                        return subprocess.CompletedProcess(args, 0, str(child.pid), "")
                    return super().call(*args, check=check)

            manager = RunningManager(self.unit_dir, backend_active=True)
            installer.backend_ready(self.port, self.binary, manager)
        finally:
            child.terminate()
            child.communicate(timeout=5)

    def test_environment_override_rejected_and_unrelated_file_preserved(self):
        environment = self.home / "voxtype.env"
        environment.write_text("UNRELATED_USER_SETTING=preserve\n")
        escaped = str(environment).replace("%", "%%")
        self.voxtype_unit.write_text(f'[Service]\nExecStart=/usr/bin/voxtype daemon\nEnvironmentFile="{escaped}"\n')
        backup = self.install()
        installer.rollback(backup, FakeManager(self.unit_dir))
        self.assertEqual(environment.read_text(), "UNRELATED_USER_SETTING=preserve\n")
        environment.write_text("VOXTYPE_WHISPER_API_KEY=not-the-local-proxy-key\n")
        with self.assertRaisesRegex(installer.InstallError, "EnvironmentFile"):
            self.install()
        self.assertEqual(self.config.read_bytes(), self.original_config)

    def test_wrong_version_rejected_before_mutation(self):
        self.release.archive = make_archive([("codex-transcribe", FAKE_BINARY.replace(b"v0.1.1", b"v0.0.0"), tarfile.REGTYPE)])
        with self.assertRaisesRegex(installer.InstallError, "pinned version"):
            self.install()
        self.assertEqual(self.config.read_bytes(), self.original_config)
        self.assertFalse(self.binary.exists())

    def test_download_and_expanded_member_caps(self):
        with patch.object(installer, "MAX_ARCHIVE", 32):
            with self.assertRaisesRegex(installer.InstallError, "cap"):
                self.install()
        with patch.object(installer, "MAX_BINARY", 32):
            with self.assertRaisesRegex(installer.InstallError, "size limit"):
                self.install()
        self.assertEqual(self.config.read_bytes(), self.original_config)

    def test_not_ready_backend_rolls_back_before_voxtype_cutover(self):
        originals = self.old_backend()
        manager = FakeManager(self.unit_dir, "enabled", True, True)
        before = {unit: dict(state) for unit, state in manager.states.items()}

        def not_ready(port, binary, user_manager):
            self.assertNotIn(("restart", installer.VOXTYPE), user_manager.calls)
            raise installer.InstallError("Backend failed its health check")

        with self.assertRaisesRegex(installer.InstallError, "prior files and service states restored"):
            self.install(no_start=False, manager=manager, readiness=not_ready)
        self.assert_originals(originals)
        self.assertEqual(manager.states, before)


class PortReuseTests(unittest.TestCase):
    def test_closed_connections_do_not_count_as_an_unrelated_listener(self):
        with socket.socket() as listener:
            listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
            listener.listen()
            self.assertFalse(installer.probe_port(port))
            with socket.create_connection(("127.0.0.1", port)) as client:
                accepted, _ = listener.accept()
                accepted.close()
                self.assertEqual(client.recv(1), b"")
        self.assertTrue(installer.probe_port(port))


if __name__ == "__main__":
    unittest.main()
