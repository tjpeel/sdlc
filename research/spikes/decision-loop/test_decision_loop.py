"""Generated, offline lifecycle and admission checks for the CLI spike."""
from concurrent.futures import ThreadPoolExecutor
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from contextlib import redirect_stdout, redirect_stderr

SCRIPT = Path(__file__).resolve().with_name("sdlc.py")
SPEC = importlib.util.spec_from_file_location("decision_loop", SCRIPT)
sdlc = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(sdlc)


class DecisionLoopTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="sdlc-decision-test-")
        self.base = Path(self.temporary.name).resolve()
        self.repo = self.base / "repo"
        self.repo.mkdir(mode=0o700)
        self.state = self.base / "state"
        self.ticket = ".sdlc/work/tickets/1/ticket-1.md"
        target = self.repo / self.ticket
        target.parent.mkdir(parents=True)
        target.write_text("# Generated fixture\n\nChoose the API behaviour. [Context](context.md)\n")
        target.with_name("context.md").write_text("Generated context only.\n")
        (self.repo / "example.txt").write_text("example\n")
        (self.repo / ".gitignore").write_text(".sdlc/work/\n")
        self.git("init", "-q")
        self.git("add", "example.txt", ".gitignore")
        self.git("-c", "user.name=Example", "-c", "user.email=example@example.invalid",
                 "-c", "commit.gpgsign=false", "commit", "-qm", "Generated fixture")
        self.add()

    def tearDown(self):
        self.temporary.cleanup()

    def git(self, *arguments):
        env = {"PATH": os.defpath, "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull}
        return subprocess.run(["git", "-C", str(self.repo), *arguments], env=env,
                              stdin=subprocess.DEVNULL, capture_output=True, check=True)

    def cli(self, *arguments, ok=True):
        output, errors = io.StringIO(), io.StringIO()
        with redirect_stdout(output), redirect_stderr(errors):
            code = sdlc.main(["--state-dir", str(self.state), *arguments])
        self.assertEqual(code, 0 if ok else 2, errors.getvalue())
        text = output.getvalue() if ok else errors.getvalue()
        return json.loads(text)

    def add(self, **overrides):
        values = {"id": "demo", "path": str(self.repo), "remote": "example/demo",
                  "runtime": "colima", "implementer": "codex", "reviewer": "claude", "check": "unit"}
        values.update(overrides)
        arguments = ["repo", "add"]
        for key, value in values.items():
            arguments.extend(["--" + key, value])
        return self.cli(*arguments, ok=not bool(overrides.get("invalid", False)))

    def start(self):
        return self.cli("job", "start", "--repo", "demo", "--ticket", self.ticket)

    def admit(self, job, **overrides):
        values = {"request": job["request_id"], "checkpoint": job["checkpoint"], "choice": "keep-api"}
        values.update({k: v for k, v in overrides.items() if k != "ok"})
        arguments = ["job", "answer", job["job_id"]]
        for key, value in values.items():
            arguments.extend(["--" + key, value])
        return self.cli(*arguments, ok=overrides.get("ok", True))

    def job_dir(self, job):
        return self.state / "jobs" / job["job_id"]

    def test_lifecycle_and_journal(self):
        job = self.start()
        self.assertTrue(job["simulation"])
        self.assertEqual(job["status"], "NEEDS_INPUT")
        question = self.cli("job", "question", job["job_id"])
        self.assertEqual(question["choices"], ["keep-api", "change-api"])
        self.assertEqual(question["checkpoint"], job["checkpoint"])
        self.assertEqual(self.admit(job)["status"], "READY_TO_CONTINUE")
        completed = self.cli("job", "continue", job["job_id"])
        self.assertEqual(completed["status"], "SIMULATED_COMPLETE")
        self.assertTrue(completed["simulation"])
        self.assertEqual(completed["result"]["checks"], "not-run")
        self.assertFalse(completed["real_work_executed"])
        self.assertEqual(completed["attempt"], 2)
        events = self.cli("job", "events", job["job_id"])["events"]
        self.assertEqual([e["event"] for e in events], ["job_started", "input_required", "answer_admitted",
                                                          "job_continued", "simulation_completed"])

    def test_continue_requires_answer_and_completion_cannot_repeat(self):
        job = self.start()
        self.cli("job", "continue", job["job_id"], ok=False)
        self.admit(job)
        self.cli("job", "continue", job["job_id"])
        self.cli("job", "continue", job["job_id"], ok=False)
        self.admit(job, ok=False)
        self.cli("job", "question", job["job_id"], ok=False)

    def test_duplicate_and_stale_answers_rejected(self):
        job = self.start()
        self.admit(job, request="0" * 32, ok=False)
        self.admit(job, checkpoint="0" * 64, ok=False)
        self.admit(job)
        self.admit(job, ok=False)

    def test_answer_from_different_job_rejected(self):
        first, second = self.start(), self.start()
        self.admit(first, request=second["request_id"], checkpoint=second["checkpoint"], ok=False)

    def test_original_ticket_change_does_not_change_admitted_snapshot(self):
        job = self.start()
        (self.repo / self.ticket).write_text("Completely different original input.\n")
        self.admit(job)
        self.assertEqual(self.cli("job", "continue", job["job_id"])["status"], "SIMULATED_COMPLETE")
        self.assertIn("Generated fixture", (self.job_dir(job) / "input" / self.ticket).read_text())

    def test_stored_snapshot_change_rejects_answer(self):
        job = self.start()
        (self.job_dir(job) / "input" / self.ticket).write_text("Changed snapshot\n")
        self.admit(job, ok=False)

    def test_stored_snapshot_change_rejects_continuation(self):
        job = self.start()
        self.admit(job)
        (self.job_dir(job) / "input" / self.ticket).write_text("Changed snapshot\n")
        self.cli("job", "continue", job["job_id"], ok=False)

    def test_checkpoint_manifest_policy_and_question_changes_rejected(self):
        for relative in ("checkpoint.json", "input/manifest.json", "policy.json", "question.json"):
            with self.subTest(relative=relative):
                job = self.start()
                file = self.job_dir(job) / relative
                data = json.loads(file.read_text())
                if relative == "question.json":
                    data["request_id"] = "0" * 32
                else:
                    data["unexpected"] = True
                file.write_text(json.dumps(data))
                self.admit(job, ok=False)

    def test_added_snapshot_file_rejected(self):
        job = self.start()
        extra = self.job_dir(job) / "input" / ".sdlc/work/tickets/1/extra.md"
        extra.write_text("Unexpected data\n")
        extra.chmod(0o600)
        self.admit(job, ok=False)

    def test_semantic_question_mutations_rejected(self):
        for field in ("question", "recommendation", "reason", "unexpected"):
            with self.subTest(field=field):
                job = self.start()
                path = self.job_dir(job) / "question.json"
                data = json.loads(path.read_text())
                data[field] = "Different content"
                path.write_text(json.dumps(data))
                self.admit(job, ok=False)

    def test_changed_head_rejected(self):
        job = self.start()
        (self.repo / "example.txt").write_text("new revision\n")
        self.git("add", "example.txt")
        self.git("-c", "user.name=Example", "-c", "user.email=example@example.invalid",
                 "-c", "commit.gpgsign=false", "commit", "-qm", "Generated change")
        self.admit(job, ok=False)

    def test_mutated_admitted_answer_rejected(self):
        for field, value in (("request_id", "0" * 32), ("choice", "change-api")):
            with self.subTest(field=field):
                job = self.start()
                self.admit(job)
                path = self.job_dir(job) / "answer.json"
                data = json.loads(path.read_text())
                data[field] = value
                path.write_text(json.dumps(data))
                self.cli("job", "continue", job["job_id"], ok=False)

    def test_state_permissions_and_repository_unchanged(self):
        before = self.git("status", "--porcelain").stdout
        job = self.start()
        self.admit(job)
        self.cli("job", "continue", job["job_id"])
        self.assertEqual(self.git("status", "--porcelain").stdout, before)
        for path in (self.state, *self.state.rglob("*")):
            self.assertEqual(path.stat().st_mode & 0o777, 0o700 if path.is_dir() else 0o600)

    def test_source_contained_state_and_relative_paths_rejected(self):
        for path in (sdlc.SOURCE_ROOT / ".decision-state", self.repo / "state", Path("relative-state")):
            with self.subTest(path=path):
                output, errors = io.StringIO(), io.StringIO()
                with redirect_stdout(output), redirect_stderr(errors):
                    code = sdlc.main(["--state-dir", str(path), "repo", "add", "--id", "test",
                                      "--path", str(self.repo), "--remote", "example/demo", "--runtime", "docker",
                                      "--implementer", "codex", "--reviewer", "claude", "--check", "unit"])
                self.assertEqual(code, 2)
                self.assertFalse((path / "repos/test.json").exists())
                self.assertFalse(path.exists())

    def test_loose_or_symlink_state_rejected(self):
        self.state.chmod(0o755)
        self.cli("repo", "list", ok=False)
        self.state.chmod(0o700)
        link = self.base / "linked-state"
        link.symlink_to(self.state, target_is_directory=True)
        with self.assertRaises(sdlc.Invalid):
            sdlc.State(link)

    def test_symlink_ticket_snapshot_and_hardlink_state_rejected(self):
        target = self.repo / self.ticket
        target.unlink()
        target.symlink_to(target.with_name("context.md"))
        self.cli("job", "start", "--repo", "demo", "--ticket", self.ticket, ok=False)
        repo_state = self.state / "repos/demo.json"
        os.link(repo_state, self.base / "hardlink.json")
        self.cli("repo", "list", ok=False)

    def test_ticket_traversal_and_job_traversal_rejected(self):
        for ticket in ("../ticket-1.md", ".sdlc/work/tickets/1/../ticket-1.md", "/.sdlc/work/tickets/1/ticket-1.md"):
            self.cli("job", "start", "--repo", "demo", "--ticket", ticket, ok=False)
        self.cli("job", "status", "../repos/demo", ok=False)

    def test_injected_text_never_rendered_or_executed(self):
        injected = "PRIVATE_MARKER\x1b[2J $(touch /tmp/sdlc-should-never-exist)"
        (self.repo / self.ticket).write_text(injected + "\n")
        job = self.start()
        question = self.cli("job", "question", job["job_id"])
        self.assertNotIn("PRIVATE_MARKER", json.dumps(question))
        self.admit(job, choice=injected, ok=False)
        self.cli("job", "status", injected, ok=False)
        self.assertEqual(question["recommendation"], "keep-api")

    def test_concurrent_answers_have_one_admission(self):
        job = self.start()
        command = ["python3", "-B", str(SCRIPT), "--state-dir", str(self.state), "job", "answer", job["job_id"],
                   "--request", job["request_id"], "--checkpoint", job["checkpoint"], "--choice", "keep-api"]
        with ThreadPoolExecutor(max_workers=2) as pool:
            results = list(pool.map(lambda _: subprocess.run(command, stdin=subprocess.DEVNULL, capture_output=True), range(2)))
        self.assertEqual(sorted(r.returncode for r in results), [0, 2])
        events = self.cli("job", "events", job["job_id"])["events"]
        self.assertEqual(sum(e["event"] == "answer_admitted" for e in events), 1)

    def test_concurrent_continuations_complete_once(self):
        job = self.start()
        self.admit(job)
        command = ["python3", "-B", str(SCRIPT), "--state-dir", str(self.state), "job", "continue", job["job_id"]]
        with ThreadPoolExecutor(max_workers=2) as pool:
            results = list(pool.map(lambda _: subprocess.run(command, stdin=subprocess.DEVNULL, capture_output=True), range(2)))
        self.assertEqual(sorted(r.returncode for r in results), [0, 2])
        self.assertEqual(self.cli("job", "status", job["job_id"])["attempt"], 2)

    def test_env_git_redirect_does_not_change_local_head(self):
        previous = os.environ.get("GIT_DIR")
        os.environ["GIT_DIR"] = "/does/not/exist"
        try:
            self.assertEqual(self.start()["status"], "NEEDS_INPUT")
        finally:
            if previous is None:
                os.environ.pop("GIT_DIR", None)
            else:
                os.environ["GIT_DIR"] = previous

    def test_cli_entrypoint_has_help(self):
        result = subprocess.run(["sh", str(SCRIPT.with_name("sdlc")), "--help"], stdin=subprocess.DEVNULL, capture_output=True)
        self.assertEqual(result.returncode, 0)
        self.assertIn(b"Offline SDLC", result.stdout)


if __name__ == "__main__":
    unittest.main()
