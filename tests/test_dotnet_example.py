"""Offline lifecycle checks for the disposable .NET fixture's Docker runner."""

import contextlib
import importlib.util
import io
import os
from pathlib import Path
import signal
import subprocess
import threading
import unittest
from unittest import mock


SCRIPT = Path(__file__).resolve().parents[1] / "examples/dotnet-smoke/scripts/integration.py"
SPEC = importlib.util.spec_from_file_location("sdlc_dotnet_example_integration", SCRIPT)
INTEGRATION = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(INTEGRATION)
SOCKET = "unix:///run/sdlc/docker.sock"


class DockerRecorder:
    def __init__(self, fail=None, cleanup_fail=None):
        self.commands = []
        self.fail = fail
        self.cleanup_fail = cleanup_fail

    def __call__(self, command, *, check, timeout, env):
        if command[:3] != ["docker", "--host", SOCKET]:
            raise AssertionError("fixture did not pin the dedicated Docker endpoint")
        if "DOCKER_CONTEXT" in env:
            raise AssertionError("fixture inherited a Docker context override")
        self.commands.append(command[3:])
        if check and self.fail is not None:
            self.fail(command[3:])
        if not check and self.cleanup_fail is not None:
            result = self.cleanup_fail(command[3:])
            if result:
                return subprocess.CompletedProcess(command, result)
        return subprocess.CompletedProcess(command, 0)


def resources(commands):
    names = set()
    for command in commands:
        if command[0] == "build":
            names.add(command[command.index("--tag") + 1])
        elif command[:2] == ["network", "create"]:
            names.add(command[-1])
        elif command[0] == "run":
            names.add(command[command.index("--name") + 1])
    return names


class DotnetExampleTests(unittest.TestCase):
    def execute(self, recorder, environment=None):
        with mock.patch.dict(os.environ, {"DOCKER_HOST": SOCKET, **(environment or {})}, clear=True), \
                mock.patch.object(INTEGRATION.signal, "signal"), \
                mock.patch.object(INTEGRATION.subprocess, "run", recorder), \
                contextlib.redirect_stderr(io.StringIO()):
            return INTEGRATION.main()

    def assert_cleanup(self, commands, expected):
        cleanup = [command for command in commands
                   if command[0] == "rm" or command[:2] in (["network", "rm"], ["image", "rm"])]
        self.assertEqual([command[-1] for command in cleanup], list(reversed(expected)))
        for command in cleanup:
            if command[0] == "rm":
                self.assertIn("--force", command)
                self.assertIn("--volumes", command)

    def planned_resources(self, commands):
        images = [command[command.index("--tag") + 1]
                  for command in commands if command[0] == "build"]
        networks = [command[-1] for command in commands if command[:2] == ["network", "create"]]
        if not networks:
            return images
        prefix = networks[0].removesuffix("-network")
        return images + networks + [prefix + "-mongo", prefix + "-api", prefix + "-tests"]

    def test_rejects_every_other_socket_before_any_docker_command(self):
        for host in (None, "", "unix:///var/run/docker.sock", "tcp://example.invalid:2375",
                     SOCKET + ".other"):
            with self.subTest(host=host), mock.patch.dict(os.environ, {}, clear=True), \
                    mock.patch.object(INTEGRATION.subprocess, "run") as command:
                if host is not None:
                    os.environ["DOCKER_HOST"] = host
                with self.assertRaisesRegex(SystemExit, "dedicated SDLC check daemon"):
                    INTEGRATION.main()
                command.assert_not_called()

    def test_success_removes_all_resources_in_reverse_and_exposes_no_host_access(self):
        docker = DockerRecorder()
        self.assertEqual(self.execute(docker), 0)
        created = resources(docker.commands)
        self.assertEqual(len(created), 6)
        self.assert_cleanup(docker.commands, self.planned_resources(docker.commands))
        runs = [command for command in docker.commands if command[0] == "run"]
        self.assertEqual(len(runs), 3)
        for command in runs:
            for option in command:
                self.assertFalse(option in ("-p", "-P", "--publish", "--publish-all", "-v", "--volume", "--mount")
                                 or option.startswith(("--publish=", "--volume=", "--mount=", "-p=", "-v=")))
            self.assertNotIn("--privileged", command)
            self.assertNotEqual(command[command.index("--network") + 1], "host")

    def test_conflicting_context_cannot_redirect_work_logs_or_cleanup(self):
        for failed in (False, True):
            with self.subTest(failed=failed):
                def fail(command):
                    if failed and command[0] == "run" and command[command.index("--name") + 1].endswith("-tests"):
                        raise subprocess.CalledProcessError(1, "fake-test")
                docker = DockerRecorder(fail=fail)
                self.assertEqual(self.execute(docker, {"DOCKER_CONTEXT": "unrelated-daemon"}), int(failed))
                self.assert_cleanup(docker.commands, self.planned_resources(docker.commands))
                if failed:
                    self.assertTrue(any(command[0] == "logs" for command in docker.commands))

    def test_build_failure_removes_attempted_images_without_starting_containers(self):
        for target in ("api", "tests"):
            with self.subTest(target=target):
                def fail(command):
                    if command[0] == "build" and command[command.index("--target") + 1] == target:
                        raise subprocess.CalledProcessError(1, ["docker", *command])
                docker = DockerRecorder(fail=fail)
                self.assertEqual(self.execute(docker), 1)
                self.assert_cleanup(docker.commands, self.planned_resources(docker.commands))
                self.assertFalse(any(command[0] == "run" for command in docker.commands))

    def test_test_failure_and_cancellation_remove_all_resources(self):
        for error in (subprocess.CalledProcessError(1, "fake-test"), KeyboardInterrupt()):
            with self.subTest(error=type(error).__name__):
                def fail(command):
                    if command[0] == "run" and command[command.index("--name") + 1].endswith("-tests"):
                        raise error
                docker = DockerRecorder(fail=fail)
                self.assertEqual(self.execute(docker), 1)
                self.assert_cleanup(docker.commands, self.planned_resources(docker.commands))

    def test_network_creation_timeout_removes_possibly_created_network(self):
        def fail(command):
            if command[:2] == ["network", "create"]:
                raise subprocess.TimeoutExpired(["docker", *command], 600)
        docker = DockerRecorder(fail=fail)
        self.assertEqual(self.execute(docker), 1)
        expected = self.planned_resources(docker.commands)[:-3]
        self.assert_cleanup(docker.commands, expected)
        self.assertFalse(any(command[0] == "run" for command in docker.commands))

    def test_cleanup_failure_changes_success_to_failure_and_continues_cleanup(self):
        for failure in ("exit", "timeout"):
            with self.subTest(failure=failure):
                def cleanup_fail(command):
                    if command[0] == "rm" and command[-1].endswith("-tests"):
                        if failure == "timeout":
                            raise subprocess.TimeoutExpired(["docker", *command], 30)
                        return 1
                docker = DockerRecorder(cleanup_fail=cleanup_fail)
                self.assertEqual(self.execute(docker), 1)
                self.assert_cleanup(docker.commands, self.planned_resources(docker.commands))

    def test_repeated_and_parallel_runs_have_disjoint_resource_names(self):
        sequential = []
        for _ in range(2):
            docker = DockerRecorder()
            self.assertEqual(self.execute(docker), 0)
            sequential.append(resources(docker.commands))
        self.assertTrue(sequential[0].isdisjoint(sequential[1]))
        parallel = [DockerRecorder(), DockerRecorder()]
        local = threading.local()
        barrier = threading.Barrier(2)
        results = []
        errors = []
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
                mock.patch.object(INTEGRATION.signal, "signal"), \
                mock.patch.object(INTEGRATION.subprocess, "run", fake_command):
            threads = [threading.Thread(target=worker, args=(recorder,)) for recorder in parallel]
            for thread in threads:
                thread.start()
            for thread in threads:
                thread.join(timeout=5)
            self.assertFalse(any(thread.is_alive() for thread in threads))
        self.assertEqual(errors, [])
        self.assertEqual(results, [0, 0])
        sets = [resources(recorder.commands) for recorder in parallel]
        self.assertTrue(sets[0].isdisjoint(sets[1]))
        for names in sets:
            self.assertEqual(len(names), 6)
            for earlier in sequential:
                self.assertTrue(names.isdisjoint(earlier))

    def test_sigterm_becomes_cancellable_interrupt(self):
        with self.assertRaises(KeyboardInterrupt):
            INTEGRATION.interrupted(signal.SIGTERM, None)


if __name__ == "__main__":
    unittest.main()
