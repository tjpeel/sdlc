"""Offline lifecycle checks for the disposable .NET fixture's Compose runner."""

import contextlib
import importlib.util
import io
import os
from pathlib import Path
import signal
import subprocess
import tempfile
import threading
import unittest
from unittest import mock


FIXTURE = Path(__file__).resolve().parents[1] / "examples/dotnet-smoke"
SPEC = importlib.util.spec_from_file_location("sdlc_dotnet_example_integration", FIXTURE / "scripts/integration.py")
INTEGRATION = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(INTEGRATION)
SOCKET = "unix:///run/sdlc/docker.sock"


class DockerRecorder:
    def __init__(self, root, fail=None, cleanup_fail=None):
        self.root = root
        self.commands = []
        self.projects = set()
        self.cleanup_profile = False
        self.fail = fail
        self.cleanup_fail = cleanup_fail

    def __call__(self, command, *, check, timeout, env):
        if command[:3] != ["docker", "--host", SOCKET] or "DOCKER_CONTEXT" in env:
            raise AssertionError("fixture did not pin the dedicated Docker endpoint")
        args = command[3:]
        if args == ["info"]:
            self.commands.append(args)
            return subprocess.CompletedProcess(command, 0)
        if args[0] != "compose":
            raise AssertionError("fixture bypassed Compose resource ownership")
        for flag, value in (("--project-directory", str(self.root)),
                            ("--env-file", str(self.root / ".env")),
                            ("--file", str(self.root / "compose.yml"))):
            if args[args.index(flag) + 1] != value:
                raise AssertionError("fixture selected a different root/configuration")
        project = args[args.index("--project-name") + 1]
        if not project.startswith("dotnet-smoke-") or len(project.removeprefix("dotnet-smoke-")) != 32:
            raise AssertionError("fixture did not use a unique Compose project")
        self.projects.add(project)
        operation = args[args.index("--file") + 2:]
        if operation[:2] == ["--profile", "integration"]:
            self.cleanup_profile = operation[2] == "down"
            operation = operation[2:]
        self.commands.append(operation)
        if check and self.fail is not None:
            self.fail(operation)
        if not check and self.cleanup_fail is not None:
            result = self.cleanup_fail(operation)
            if result:
                return subprocess.CompletedProcess(command, result)
        return subprocess.CompletedProcess(command, 0)


class DotnetExampleTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name).resolve()
        (self.root / "scripts").mkdir()
        (self.root / ".env").write_text("SMOKE_ENV_FILE_MARKER=disposable-test-value\n")

    def execute(self, recorder, environment=None):
        with mock.patch.dict(os.environ, {"DOCKER_HOST": SOCKET, **(environment or {})}, clear=True), \
                mock.patch.object(INTEGRATION, "__file__", str(self.root / "scripts/integration.py")), \
                mock.patch.object(INTEGRATION.signal, "signal"), \
                mock.patch.object(INTEGRATION.subprocess, "run", recorder), \
                contextlib.redirect_stderr(io.StringIO()) as diagnostics:
            result = INTEGRATION.main()
        self.assertNotIn("disposable-test-value", diagnostics.getvalue())
        return result

    def assert_cleanup(self, recorder):
        self.assertEqual(recorder.commands[-1], ["down", "--volumes", "--remove-orphans", "--rmi", "local"])
        self.assertEqual(sum(command[0] == "down" for command in recorder.commands), 1)
        self.assertEqual(len(recorder.projects), 1)
        self.assertTrue(recorder.cleanup_profile, "cleanup must include profiled test images")

    def test_rejects_every_other_socket_before_any_docker_command(self):
        for host in (None, "", "unix:///var/run/docker.sock", "tcp://example.invalid:2375", SOCKET + ".other"):
            with self.subTest(host=host), mock.patch.dict(os.environ, {}, clear=True), \
                    mock.patch.object(INTEGRATION.subprocess, "run") as command:
                if host is not None:
                    os.environ["DOCKER_HOST"] = host
                with self.assertRaisesRegex(SystemExit, "dedicated SDLC check daemon"):
                    INTEGRATION.main()
                command.assert_not_called()

    def test_requires_regular_root_env_before_any_docker_command(self):
        path = self.root / ".env"
        path.unlink()
        for kind in ("missing", "directory", "symlink"):
            with self.subTest(kind=kind):
                if kind == "directory":
                    path.mkdir()
                elif kind == "symlink":
                    target = self.root / "other.env"
                    target.write_text("DISPOSABLE=test\n")
                    path.symlink_to(target)
                docker = DockerRecorder(self.root)
                with self.assertRaisesRegex(SystemExit, "regular, non-symlink root .env"):
                    self.execute(docker)
                self.assertEqual(docker.commands, [])
                if path.is_symlink():
                    path.unlink()
                elif path.is_dir():
                    path.rmdir()

    def test_success_uses_compose_services_and_project_cleanup_without_logs(self):
        docker = DockerRecorder(self.root)
        self.assertEqual(self.execute(docker), 0)
        self.assertEqual(docker.commands[:-1], [["info"], ["build", "api", "tests", "host-tests"],
                                              ["up", "-d", "mongo", "api"],
                                              ["exec", "-T", "api", "/bin/sh", "-c", "test ! -e /source/.env && test ! -e /app/.env"],
                                              ["run", "--rm", "--no-deps", "tests"],
                                              ["run", "--rm", "--no-deps", "host-tests"]])
        self.assert_cleanup(docker)
        self.assertFalse(any(command[0] in ("logs", "config") for command in docker.commands))

    def test_conflicting_context_cannot_redirect_work_logs_or_cleanup(self):
        def fail(command):
            if command[0] == "run":
                raise subprocess.CalledProcessError(1, "fake-test")
        docker = DockerRecorder(self.root, fail=fail)
        self.assertEqual(self.execute(docker, {"DOCKER_CONTEXT": "unrelated-daemon"}), 1)
        self.assertIn(["logs", "--no-color", "--tail", "100", "mongo", "api"], docker.commands)
        self.assert_cleanup(docker)

    def test_build_start_and_test_failures_and_cancellation_always_clean_project(self):
        for operation in ("build", "up", "exec", "run", "host-tests"):
            for cancelled in (False, True):
                with self.subTest(operation=operation, cancelled=cancelled):
                    def fail(command):
                        if command[0] == operation or (operation == "host-tests" and command[0] == "run" and command[-1] == "host-tests"):
                            if cancelled:
                                raise KeyboardInterrupt
                            raise subprocess.CalledProcessError(1, "disposable-test-value")
                    docker = DockerRecorder(self.root, fail=fail)
                    self.assertEqual(self.execute(docker), 1)
                    self.assert_cleanup(docker)
                    self.assertEqual(docker.commands[-2], ["logs", "--no-color", "--tail", "100", "mongo", "api"])
                    if operation == "exec":
                        self.assertFalse(any(command[0] == "run" for command in docker.commands))
                    if operation == "build":
                        self.assertFalse(any(command[0] in ("up", "run") for command in docker.commands))

    def test_cleanup_failure_propagates_and_failed_logs_do_not_skip_cleanup(self):
        for failure in ("exit", "timeout"):
            with self.subTest(failure=failure):
                def cleanup_fail(command):
                    if command[0] == "down":
                        if failure == "timeout":
                            raise subprocess.TimeoutExpired("disposable-test-value", 60)
                        return 1
                docker = DockerRecorder(self.root, cleanup_fail=cleanup_fail)
                self.assertEqual(self.execute(docker), 1)
                self.assert_cleanup(docker)
        def fail(command):
            if command[0] == "run":
                raise KeyboardInterrupt
        def fail_logs(command):
            if command[0] == "logs":
                raise subprocess.TimeoutExpired("disposable-test-value", 15)
        docker = DockerRecorder(self.root, fail=fail, cleanup_fail=fail_logs)
        self.assertEqual(self.execute(docker), 1)
        self.assert_cleanup(docker)

    def test_repeated_and_parallel_runs_have_different_project_names(self):
        recorders = [DockerRecorder(self.root) for _ in range(4)]
        for recorder in recorders[:2]:
            self.assertEqual(self.execute(recorder), 0)
        local = threading.local()
        barrier = threading.Barrier(2)
        results, errors = [], []
        def fake_command(command, **kwargs):
            if command == ["docker", "--host", SOCKET, "info"]:
                barrier.wait(timeout=3)
            return local.recorder(command, **kwargs)
        def worker(recorder):
            local.recorder = recorder
            try:
                results.append(INTEGRATION.main())
            except BaseException as error:
                errors.append(error)
        with mock.patch.dict(os.environ, {"DOCKER_HOST": SOCKET}, clear=True), \
                mock.patch.object(INTEGRATION, "__file__", str(self.root / "scripts/integration.py")), \
                mock.patch.object(INTEGRATION.signal, "signal"), \
                mock.patch.object(INTEGRATION.subprocess, "run", fake_command):
            threads = [threading.Thread(target=worker, args=(recorder,)) for recorder in recorders[2:]]
            for thread in threads:
                thread.start()
            for thread in threads:
                thread.join(timeout=5)
            self.assertFalse(any(thread.is_alive() for thread in threads))
        self.assertEqual(errors, [])
        self.assertEqual(results, [0, 0])
        self.assertEqual(len(set.union(*(recorder.projects for recorder in recorders))), 4)
        for recorder in recorders:
            self.assert_cleanup(recorder)

    def test_compose_uses_env_file_and_required_interpolation_without_host_access(self):
        import re
        compose = (FIXTURE / "compose.yml").read_text()
        for key in ("volumes", "container_name", "privileged"):
            self.assertIsNone(re.search(r"^\s*" + key + r"\s*:", compose, re.MULTILINE))
        # These ports live on the nested daemon, whose own container publishes
        # no engine-host ports. The second test service exercises localhost.
        ports = re.findall(r'^\s*- "([0-9:]+)"$', compose, re.MULTILINE)
        self.assertEqual(set(ports), {"18080:8080", "27017:27017"})
        self.assertEqual(compose.count("network_mode: host"), 1)
        self.assertIn("SMOKE_API_URL: http://api:8080", compose)
        self.assertIn("SMOKE_API_URL: http://localhost:18080", compose)
        self.assertEqual(compose.count("env_file:\n      - .env"), 2)
        self.assertNotIn("docker.sock", compose)
        self.assertIn("image: mongo:8.0.16", compose)
        self.assertEqual(compose.count("profiles: [integration]"), 2)
        self.assertIn("env_file:\n      - .env", compose)
        for variable in ("SMOKE_MONGO_CONNECTION_STRING", "SMOKE_COMPOSE_MARKER"):
            self.assertIn("${" + variable + ":?", compose)
        ignored = (FIXTURE / ".dockerignore").read_text().splitlines()
        self.assertIn(".env", ignored)
        self.assertIn(".env.*", ignored)

    def test_sigterm_becomes_cancellable_interrupt(self):
        with self.assertRaises(KeyboardInterrupt):
            INTEGRATION.interrupted(signal.SIGTERM, None)


if __name__ == "__main__":
    unittest.main()
