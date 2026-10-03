"""Generated files and fakes only; never inspect host keys or running services."""
import contextlib
import errno
import importlib.util
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import tempfile
import unittest
from unittest import mock


SPEC = importlib.util.spec_from_file_location("host_control_probe", Path(__file__).with_name("probe.py"))
probe = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(probe)


class ProbeFixture(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="host-control-generated-")
        self.addCleanup(self.temporary.cleanup)
        self.folder = Path(self.temporary.name)
        self.trial = "host-control-0123456789ab"
        self.state = self.folder / self.trial
        self.copied = self.folder / (self.trial + "-copy")
        self.state.mkdir(mode=0o700)
        self.copied.mkdir(mode=0o700)
        self.canary = self.state / "private-canary.txt"
        self.canary.write_text("GENERATED_MARKER_ONLY\n")
        self.canary.chmod(0o600)
        self.input = self.copied / "protected-input.txt"
        self.input.write_text("GENERATED_PUBLIC_INPUT\n")
        (self.copied / "bin").mkdir()
        for name in ("colima", "limactl"):
            (self.copied / "bin" / name).write_text("Generated nonexecuted binary placeholder.\n")
        self.manifest = {
            "schema_version": 1, "trial_id": self.trial, "manager_uid": max(1, os.getuid()),
            "manager_pid": 424242, "manager_state_root": str(self.state), "manager_copy_root": str(self.copied),
            "targets": {"private_canary": str(self.canary), "protected_input": str(self.input),
                        "docker_socket": str(self.state / "colima" / "trial" / "docker.sock")},
            "worker": {"id": "a" * 64, "label_key": "sdlc.host-control.trial", "label_value": self.trial},
            "cli": {"colima_binary": str(self.copied / "bin" / "colima"), "profile": "trial",
                    "lima_binary": str(self.copied / "bin" / "limactl"), "instance": "colima-trial"},
            "ssh": {"host": "127.0.0.1", "port": 23456, "username": "generated_guest",
                    "identity_file": str(self.state / "lima" / "_config" / "user")},
            "admission": {"trial_id": self.trial, "base_commit": "b" * 40, "ticket_sha256": "c" * 64,
                          "job_id": "d" * 32, "worker_id": "a" * 64},
            "positive_controls": {"guest_ssh": True, "docker_ping": True,
                                  "worker_inspect": True, "worker_exec": True},
        }
        self.manifest_file = self.folder / "public-manifest.json"
        self.save()

    def save(self):
        self.manifest_file.write_text(json.dumps(self.manifest))

    def test_generated_manifest_and_optional_ssh_validate(self):
        self.assertEqual(probe.load_manifest(self.manifest_file), self.manifest)

    def test_rejects_unknown_fields_and_unsafe_paths(self):
        mutations = [
            lambda value: value.update(command="cat /private-key"),
            lambda value: value.update(trial_id="existing-colima"),
            lambda value: value["targets"].update(private_canary="/unrelated/private-key"),
            lambda value: value["targets"].update(private_canary=str(self.state / ".." / "elsewhere")),
            lambda value: value["worker"].update(id="a" * 12),
            lambda value: value["worker"].update(label_value="unrelated-trial"),
            lambda value: value["cli"].update(profile="default"),
            lambda value: value["cli"].update(colima_binary="/arbitrary/program"),
            lambda value: value["ssh"].update(host="example.invalid"),
            lambda value: value["ssh"].update(port=22),
            lambda value: value["ssh"].update(identity_file="/unrelated/private-key"),
            lambda value: value["admission"].update(worker_id="b" * 64),
            lambda value: value["admission"].update(base_commit="fake"),
            lambda value: value["positive_controls"].update(worker_exec="true"),
        ]
        for mutate in mutations:
            with self.subTest(mutation=mutate):
                value = json.loads(json.dumps(self.manifest))
                mutate(value)
                self.manifest_file.write_text(json.dumps(value))
                with self.assertRaises(probe.ManifestError):
                    probe.load_manifest(self.manifest_file)

    def test_rejects_visible_symlink_escape_and_symlink_manifest(self):
        outside = self.folder / "generated-outside.txt"
        outside.write_text("GENERATED_OUTSIDE\n")
        self.canary.unlink()
        self.canary.symlink_to(outside)
        with self.assertRaises(probe.ManifestError):
            probe.load_manifest(self.manifest_file)
        link = self.folder / "manifest-link.json"
        link.symlink_to(self.manifest_file)
        with self.assertRaises(probe.ManifestError):
            probe.load_manifest(link)

    def test_rejects_duplicate_fields_oversized_and_deep_json(self):
        for content in ('{"schema_version":1,"schema_version":1}',
                        " " * (probe.MAX_MANIFEST_BYTES + 1), "[" * 1200 + "]" * 1200):
            self.manifest_file.write_text(content)
            with self.assertRaises(probe.ManifestError):
                probe.load_manifest(self.manifest_file)

    def test_read_and_write_open_never_disclose_or_change_generated_content(self):
        before = self.canary.read_bytes()
        result = probe.file_probe("read", self.canary, self.state)
        self.assertEqual(result["outcome"], probe.OBTAINED)
        result = probe.file_probe("write", self.canary, self.state, write=True)
        self.assertEqual(result["outcome"], probe.OBTAINED)
        self.assertEqual(self.canary.read_bytes(), before)
        self.assertNotIn("GENERATED_MARKER", json.dumps(result))

    def test_file_missing_is_inconclusive_denied_errno_is_evidence(self):
        result = probe.file_probe("read", self.state / "missing", self.state)
        self.assertEqual(result["outcome"], probe.INCONCLUSIVE)
        for code in (errno.EACCES, errno.EPERM):
            with mock.patch.object(probe.os, "open", side_effect=OSError(code, "generated failure")):
                result = probe.file_probe("write", self.canary, self.state, write=True)
            self.assertEqual(result["outcome"], probe.DENIED)

    def test_generated_fifo_is_rejected_without_blocking(self):
        fifo = self.state / "generated-fifo"
        os.mkfifo(fifo)
        self.assertEqual(probe.file_probe("read", fifo, self.state)["outcome"], probe.INCONCLUSIVE)
        with self.assertRaises(probe.ManifestError):
            probe.load_manifest(fifo)

    def test_endpoint_deadline_applies_to_later_reads(self):
        # This creates an unconnected disposable socket and touches no service.
        with probe.DeadlineSocket() as connection:
            connection.deadline = 0
            with self.assertRaises(TimeoutError):
                connection.recv_into(bytearray(1))

    def test_docker_uses_only_ping_and_exact_owned_worker_inspect(self):
        socket_path = self.state / "colima" / "trial" / "docker.sock"
        socket_path.parent.mkdir(parents=True)
        socket_path.write_text("Generated socket placeholder; never connected.\n")
        body = json.dumps({"Id": "a" * 64, "Config": {"Labels": {"sdlc.host-control.trial": self.trial}}}).encode()
        with mock.patch.object(probe, "docker_get", side_effect=[(200, b"OK"), (200, body)]) as get:
            results = probe.docker_probes(self.manifest)
        self.assertEqual([item["outcome"] for item in results], [probe.OBTAINED, probe.OBTAINED])
        self.assertEqual([call.args[1] for call in get.call_args_list],
                         ["/_ping", "/containers/" + "a" * 64 + "/json"])
        self.assertNotIn("GENERATED", json.dumps(results))

    def test_docker_missing_refused_and_timeout_cannot_pass(self):
        with mock.patch.object(probe, "reject_symlinks"):
            for code in (errno.ENOENT, errno.ECONNREFUSED, errno.ETIMEDOUT):
                with self.subTest(errno=code), mock.patch.object(probe, "docker_get", side_effect=OSError(code, "generated")):
                    self.assertEqual(probe.docker_probes(self.manifest)[0]["outcome"], probe.INCONCLUSIVE)
            with mock.patch.object(probe, "docker_get", side_effect=PermissionError(errno.EACCES, "generated")):
                self.assertEqual(probe.docker_probes(self.manifest)[0]["outcome"], probe.DENIED)

    def test_signal_uses_zero_and_missing_pid_cannot_pass(self):
        with mock.patch.object(probe.os, "kill") as kill:
            self.assertEqual(probe.signal_probe(424242)["outcome"], probe.OBTAINED)
            kill.assert_called_once_with(424242, 0)
        with mock.patch.object(probe.os, "kill", side_effect=ProcessLookupError(errno.ESRCH, "generated")):
            self.assertEqual(probe.signal_probe(424242)["outcome"], probe.INCONCLUSIVE)

    def test_cli_and_ssh_are_fixed_and_environment_has_no_ambient_credentials(self):
        scratch = self.folder / "scratch"
        scratch.mkdir()
        (self.state / "colima").mkdir()
        with mock.patch.dict(probe.os.environ, {"SSH_AUTH_SOCK": "/fake-agent", "GH_TOKEN": "FAKE_TEST_TOKEN",
                                             "HOME": "/fake-client-home", "DOCKER_HOST": "fake-daemon"}), \
                mock.patch.object(probe, "command_probe", return_value={}) as command:
            probe.cli_probes(self.manifest, scratch, raw_ssh=True)
        calls = command.call_args_list
        self.assertEqual(calls[0].args[1], [self.manifest["cli"]["colima_binary"], "ssh", "--profile", "trial", "--", "true"])
        self.assertEqual(calls[1].args[1], [self.manifest["cli"]["lima_binary"], "shell", "colima-trial", "true"])
        ssh = calls[2].args[1]
        self.assertEqual(ssh[-1], "true")
        for option in ("BatchMode=yes", "IdentityAgent=none", "IdentitiesOnly=yes", "ForwardAgent=no", "ControlPath=none"):
            self.assertIn(option, ssh)
        self.assertEqual(ssh[ssh.index("-i") + 1], self.manifest["ssh"]["identity_file"])
        for call in calls:
            env = call.args[2]
            self.assertEqual(env["HOME"], "/fake-client-home")
            self.assertEqual(env["COLIMA_HOME"], str(scratch / "colima-client"))
            self.assertEqual(env["LIMA_HOME"], str(self.state / "lima"))
            self.assertFalse(set(env) & {"SSH_AUTH_SOCK", "GH_TOKEN", "DOCKER_HOST", "OPENAI_API_KEY"})

    def test_colima_does_not_launch_when_disposable_client_state_is_unavailable(self):
        scratch = self.folder / "scratch"
        scratch.mkdir()
        for failure in (PermissionError(errno.EACCES, "generated"), FileNotFoundError(errno.ENOENT, "generated")):
            with mock.patch.object(probe, "reject_symlinks", side_effect=failure), \
                    mock.patch.object(probe, "command_probe", return_value={}) as command:
                results = probe.cli_probes(self.manifest, scratch)
            self.assertEqual(results[0]["outcome"], probe.INCONCLUSIVE)
            self.assertEqual(results[0]["reason"], "state_unavailable_client_fallback_risk")
            self.assertEqual([call.args[0] for call in command.call_args_list], ["lima_guest_true"])

    def test_missing_home_is_preserved_without_fabricating_a_home_directory(self):
        scratch = self.folder / "scratch"
        scratch.mkdir()
        with mock.patch.dict(probe.os.environ, {}, clear=True):
            env = probe.sanitized_environment(self.manifest, scratch, self.manifest["cli"]["colima_binary"])
        self.assertNotIn("HOME", env)
        self.assertEqual(env["LIMA_HOME"], str(self.state / "lima"))

    def test_cli_permission_error_with_refused_connection_is_inconclusive(self):
        scratch = self.folder / "scratch"
        scratch.mkdir()
        outputs = [(b"Permission denied\n", probe.DENIED),
                   (b"Permission denied opening generated key\nConnection refused\n", probe.INCONCLUSIVE),
                   (b"No such file or directory\n", probe.INCONCLUSIVE)]
        for diagnostic, expected in outputs:
            class FakeProcess:
                def __init__(self, command, **kwargs):
                    kwargs["stderr"].write(diagnostic)
                    self.returncode = 1

                def wait(self, timeout):
                    return self.returncode

            with self.subTest(output=diagnostic), mock.patch.object(probe.subprocess, "Popen", FakeProcess):
                result = probe.command_probe("generated", ["generated-only"], {}, scratch)
            self.assertEqual(result["outcome"], expected)
            self.assertNotIn("generated key", json.dumps(result))

    def test_client_launch_permission_denial_is_inconclusive(self):
        scratch = self.folder / "scratch"
        scratch.mkdir()
        with mock.patch.object(probe.subprocess, "Popen", side_effect=PermissionError(errno.EACCES, "generated")):
            result = probe.command_probe("generated", ["generated-only"], {}, scratch)
        self.assertEqual(result["outcome"], probe.INCONCLUSIVE)
        self.assertEqual(result["reason"], "client_could_not_start")

    def test_initiator_cli_override_is_owned_regular_executable_in_trial_copy(self):
        binary = self.copied / "bin" / "colima"
        binary.chmod(0o755)
        with mock.patch.object(probe.os, "getuid", return_value=binary.stat().st_uid):
            self.assertEqual(probe.client_override(binary, self.trial, "colima"), str(binary))
        binary.chmod(0o777)
        with self.assertRaises(probe.ManifestError):
            probe.client_override(binary, self.trial, "colima")
        with self.assertRaises(probe.ManifestError):
            probe.client_override(self.folder / "arbitrary-program", self.trial, "colima")

    def test_own_repository_copy_is_allowed_only_for_client_override(self):
        # Resolve the generated paths so macOS /tmp and /var aliases cannot mask
        # the equal-root case seen when running the probe from its own bundle.
        copied = self.copied.resolve()
        binary = copied / "bin" / "colima"
        binary.chmod(0o755)
        with mock.patch.object(probe, "REPOSITORY", copied), \
                mock.patch.object(probe.os, "getuid", return_value=binary.stat().st_uid):
            self.assertEqual(probe.client_override(binary, self.trial, "colima"), str(binary))
            with self.assertRaises(probe.ManifestError):
                probe.root_path(str(copied), self.trial + "-copy")
            with self.assertRaises(probe.ManifestError):
                probe.load_manifest(self.manifest_file)
        with mock.patch.object(probe, "REPOSITORY", self.state.resolve()):
            with self.assertRaises(probe.ManifestError):
                probe.load_manifest(self.manifest_file)

    def test_manager_root_cannot_use_a_symlink_alias_of_the_repository(self):
        alias_parent = self.folder / "generated-alias"
        alias_parent.symlink_to(self.folder, target_is_directory=True)
        alias_copy = alias_parent / (self.trial + "-copy")
        with mock.patch.object(probe, "REPOSITORY", self.copied.resolve()):
            with self.assertRaises(probe.ManifestError):
                probe.root_path(str(alias_copy), self.trial + "-copy")

    def test_uid_root_admin_and_known_sandbox_guards(self):
        self.manifest["manager_uid"] = 2002
        with mock.patch.dict(probe.os.environ, {}, clear=True), mock.patch.object(probe.os, "getuid", return_value=2001), \
                mock.patch.object(probe.os, "getgroups", return_value=[20]):
            self.assertEqual(probe.guard_identity(self.manifest, False, True), 2001)
            with self.assertRaises(probe.ManifestError):
                probe.guard_identity(self.manifest, False, False)
        for uid, groups in ((2002, [20]), (0, [0]), (2001, [20, 80])):
            with mock.patch.dict(probe.os.environ, {}, clear=True), mock.patch.object(probe.os, "getuid", return_value=uid), \
                    mock.patch.object(probe.os, "getgroups", return_value=groups):
                with self.assertRaises(probe.ManifestError):
                    probe.guard_identity(self.manifest, False, True)
        with mock.patch.dict(probe.os.environ, {"CODEX_SANDBOX": "seatbelt"}, clear=True):
            with self.assertRaises(probe.ManifestError):
                probe.guard_identity(self.manifest, False, True)

    def test_same_user_control_never_establishes_protection_and_output_is_private(self):
        output = self.folder / "control.json"
        with mock.patch.dict(probe.os.environ, {}, clear=True), \
                mock.patch.object(probe.os, "getuid", return_value=self.manifest["manager_uid"]), \
                mock.patch.object(probe, "probe", return_value=[probe.observation("fake", probe.DENIED, "generated")]), \
                contextlib.redirect_stdout(io.StringIO()) as stdout:
            code = probe.main(["--manifest", str(self.manifest_file), "--result", str(output),
                               "--ordinary-shell", "--same-user-control"])
        self.assertEqual(code, 0)
        report = json.loads(output.read_text())
        self.assertEqual(report["summary"], "CONTROL")
        self.assertFalse(report["protection_established"])
        self.assertEqual(stat.S_IMODE(output.stat().st_mode), 0o600)
        self.assertNotIn("GENERATED_MARKER", stdout.getvalue())

    def test_any_obtained_access_fails_and_inconclusive_prevents_denied_summary(self):
        self.manifest["manager_uid"] = 2002
        self.save()
        for outcome, summary, exit_code in ((probe.OBTAINED, "FAILURE", 1),
                                           (probe.INCONCLUSIVE, "INCONCLUSIVE", 2),
                                           (probe.DENIED, "DENIED", 0)):
            output = self.folder / (summary + ".json")
            with mock.patch.dict(probe.os.environ, {}, clear=True), \
                    mock.patch.object(probe.os, "getuid", return_value=2001), \
                    mock.patch.object(probe.os, "getgroups", return_value=[20]), \
                    mock.patch.object(probe, "probe", return_value=[probe.observation("fake", outcome, "generated")]), \
                    contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(probe.main(["--manifest", str(self.manifest_file), "--result", str(output),
                                             "--ordinary-shell"]), exit_code)
            report = json.loads(output.read_text())
            self.assertEqual(report["summary"], summary)
            self.assertFalse(report["protection_established"])
            self.assertTrue(report["complete_trial_pending"])
            self.assertEqual(report["probe_denials_established"], summary == "DENIED")

    def test_missing_or_failed_manager_positive_controls_cannot_establish_denials(self):
        self.manifest["manager_uid"] = 2002
        for controls in (None, {"guest_ssh": True, "docker_ping": True, "worker_inspect": True, "worker_exec": False}):
            if controls is None:
                self.manifest.pop("positive_controls", None)
            else:
                self.manifest["positive_controls"] = controls
            self.save()
            output = self.folder / ("missing-controls.json" if controls is None else "failed-controls.json")
            with mock.patch.dict(probe.os.environ, {}, clear=True), \
                    mock.patch.object(probe.os, "getuid", return_value=2001), \
                    mock.patch.object(probe.os, "getgroups", return_value=[20]), \
                    mock.patch.object(probe, "probe", return_value=[probe.observation("fake", probe.DENIED, "generated")]), \
                    contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(probe.main(["--manifest", str(self.manifest_file), "--result", str(output),
                                             "--ordinary-shell"]), 2)
            self.assertEqual(json.loads(output.read_text())["summary"], "INCONCLUSIVE")

    def test_management_key_probe_uses_only_generated_exact_path_without_disclosure(self):
        key = self.state / "lima" / "_config" / "user"
        key.parent.mkdir(parents=True)
        key.write_text("GENERATED_FAKE_KEY_NOT_A_CREDENTIAL\n")
        with mock.patch.object(probe, "docker_probes", return_value=[]), \
                mock.patch.object(probe, "signal_probe", return_value={}), \
                mock.patch.object(probe, "cli_probes", return_value=[]):
            results = probe.probe(self.manifest, self.folder)
        result = next(item for item in results if item.get("probe") == "management_key_read")
        self.assertEqual(result["outcome"], probe.OBTAINED)
        self.assertNotIn("GENERATED_FAKE_KEY", json.dumps(results))

    def test_output_rejects_repository_manager_roots_and_existing_file(self):
        for path in (probe.REPOSITORY / "generated-result.json", self.state / "result.json",
                     self.copied / "result.json", self.manifest_file):
            with self.subTest(path=path), self.assertRaises(probe.ManifestError):
                probe.result_path(path, self.manifest)


if __name__ == "__main__":
    unittest.main()
