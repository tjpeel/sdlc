#!/usr/bin/env python3
"""Offline SDLC decision-loop simulation. It never invokes a model or a runner."""
import argparse
from contextlib import contextmanager
import hashlib
import importlib.util
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import subprocess
import sys
import time
import uuid

try:
    import fcntl
except ImportError:
    fcntl = None

SOURCE_ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("decision_ticket_input", SOURCE_ROOT / "runtime/bin/ticket_input.py")
ticket_input = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ticket_input)
SLUG = re.compile(r"[a-z][a-z0-9-]{0,39}")
ID = re.compile(r"[a-f0-9]{32}")
SHA = re.compile(r"[a-f0-9]{64}")
REMOTE = re.compile(r"[A-Za-z0-9][A-Za-z0-9_-]{0,38}/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}")
CHECK = re.compile(r"[a-z][a-z0-9-]{0,31}")
RUNTIMES = ("docker", "colima", "remote-vm", "ubuntu-box")
PROVIDERS = ("codex", "claude")
CHOICES = ("keep-api", "change-api")
MAX_JSON = 1024 * 1024


class Invalid(Exception):
    pass


def canonical(value):
    return (json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True) + "\n").encode()


def digest(value):
    return hashlib.sha256(value).hexdigest()


def identifier(value, pattern=ID):
    if not isinstance(value, str) or not pattern.fullmatch(value):
        raise Invalid("Invalid identifier.")
    return value


def duplicates(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise Invalid("Duplicate fields are not accepted.")
        result[key] = value
    return result


def path_without_links(value):
    candidate = Path(value)
    if not candidate.is_absolute() or any(p in (".", "..") for p in candidate.parts):
        raise Invalid("Paths must be absolute without traversal.")
    if any(c in str(candidate) for c in "\x00\r\n"):
        raise Invalid("Unsafe path.")
    for part in (candidate, *candidate.parents):
        try:
            if stat.S_ISLNK(part.lstat().st_mode):
                raise Invalid("Symbolic links are not accepted in paths.")
        except FileNotFoundError:
            continue
    return candidate


def beneath(path, root):
    return path == root or root in path.parents


def private_file(path, maximum=MAX_JSON):
    path_without_links(path)
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        info = os.fstat(descriptor)
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid()
                or info.st_nlink != 1 or info.st_mode & 0o077 or info.st_size > maximum):
            raise Invalid("State files must be bounded, private regular files owned by this account.")
        content = bytearray()
        while len(content) <= maximum:
            block = os.read(descriptor, min(65536, maximum + 1 - len(content)))
            if not block:
                return bytes(content)
            content.extend(block)
        raise Invalid("State file is too large.")
    finally:
        os.close(descriptor)


def read_json(path):
    value = json.loads(private_file(path), object_pairs_hook=duplicates)
    if not isinstance(value, dict):
        raise Invalid("Expected a state object.")
    return value


def write_json(path, value, replace=False):
    data = canonical(value)
    temporary = path.parent / (".write-" + uuid.uuid4().hex)
    descriptor = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    try:
        with os.fdopen(descriptor, "wb") as output:
            output.write(data)
            output.flush()
            os.fsync(output.fileno())
        if replace:
            private_file(path)
            os.replace(temporary, path)
        else:
            os.link(temporary, path, follow_symlinks=False)
            temporary.unlink()
    finally:
        temporary.unlink(missing_ok=True)


def local_head(repo):
    # rev-parse does not execute hooks, fetch, or read credential helpers. Global
    # config and ambient Git variables are excluded; no repository scripts run.
    env = {"PATH": os.defpath, "LC_ALL": "C", "GIT_CONFIG_NOSYSTEM": "1",
           "GIT_CONFIG_GLOBAL": os.devnull, "GIT_TERMINAL_PROMPT": "0", "GIT_OPTIONAL_LOCKS": "0"}
    common = ["git", "-c", "core.hooksPath=" + os.devnull,
              "-c", "safe.directory=" + str(repo), "-C", str(repo), "rev-parse"]
    outputs = []
    for arguments in (["--show-toplevel"], ["--verify", "HEAD^{commit}"]):
        result = subprocess.run(common + arguments, env=env, stdin=subprocess.DEVNULL,
                                capture_output=True, timeout=10, check=False)
        if result.returncode or len(result.stdout) > 4096:
            raise Invalid("Expected a local Git repository with a committed HEAD.")
        outputs.append(result.stdout.decode("utf-8").strip())
    if outputs[0] != str(repo) or not re.fullmatch(r"[a-f0-9]{40}|[a-f0-9]{64}", outputs[1]):
        raise Invalid("Path must be the Git repository root.")
    return outputs[1]


class State:
    def __init__(self, root):
        self.root = path_without_links(root)
        if (beneath(self.root, SOURCE_ROOT) or ".git" in self.root.parts
                or any((parent / ".git").exists() or (parent / ".git").is_symlink() for parent in self.root.parents)):
            raise Invalid("State must be outside this source checkout and Git metadata.")
        self.root.mkdir(mode=0o700, parents=False, exist_ok=True)
        self.check_dir(self.root)
        for name in ("repos", "jobs"):
            directory = self.root / name
            directory.mkdir(mode=0o700, exist_ok=True)
            self.check_dir(directory)

    @staticmethod
    def check_dir(path):
        path_without_links(path)
        info = path.stat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
            raise Invalid("State directories must have mode 0700 and belong to this account.")

    @contextmanager
    def lock(self):
        if fcntl is None:
            raise Invalid("This spike needs POSIX advisory locks; use Linux, macOS or WSL.")
        path = self.root / ".lock"
        descriptor = os.open(path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
        try:
            info = os.fstat(descriptor)
            if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_nlink != 1 or info.st_mode & 0o077:
                raise Invalid("Unsafe state lock.")
            fcntl.flock(descriptor, fcntl.LOCK_EX)
            yield
        finally:
            os.close(descriptor)

    def repo_path(self, repo_id):
        return self.root / "repos" / (identifier(repo_id, SLUG) + ".json")

    def load_repo(self, repo_id):
        return read_json(self.repo_path(repo_id))

    def job_dir(self, job_id):
        path = self.root / "jobs" / identifier(job_id)
        self.check_dir(path)
        return path

    def load_job(self, job_id):
        return read_json(self.job_dir(job_id) / "job.json")

    def event(self, directory, job, event_type):
        value = {"schema_version": 1, "simulation": True, "job_id": job["job_id"],
                 "event": event_type, "attempt": job["attempt"], "status": job["status"],
                 "time_unix": int(time.time()), "checkpoint": job["checkpoint"],
                 "request_id": job["request_id"]}
        path = directory / "events.jsonl"
        descriptor = os.open(path, os.O_WRONLY | os.O_APPEND | os.O_CREAT | os.O_NOFOLLOW, 0o600)
        try:
            info = os.fstat(descriptor)
            if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_nlink != 1 or info.st_mode & 0o077:
                raise Invalid("Unsafe event journal.")
            os.write(descriptor, canonical(value))
            os.fsync(descriptor)
        finally:
            os.close(descriptor)


def onboard(state, args):
    repo_id = identifier(args.id, SLUG)
    identifier(args.remote, REMOTE)
    if args.remote.split("/")[1] in (".", "..") or args.remote.endswith(".git"):
        raise Invalid("Use an OWNER/REPO identifier without a .git suffix.")
    checks = args.check
    if not checks or len(checks) > 8 or len(set(checks)) != len(checks):
        raise Invalid("Supply one to eight distinct descriptive check labels.")
    for check in checks:
        identifier(check, CHECK)
    repo = path_without_links(args.path)
    if not repo.is_dir() or beneath(state.root, repo) or beneath(repo, state.root):
        raise Invalid("Repository and private state must be separate directories.")
    if args.implementer == args.reviewer:
        raise Invalid("Select different implementation and review providers.")
    local_head(repo)
    value = {"schema_version": 1, "simulation": True, "repo_id": repo_id,
             "repo_path": str(repo), "remote": args.remote,
             "policy": {"runtime": args.runtime, "implementer": args.implementer,
                        "reviewer": args.reviewer, "checks": checks,
                        "publication": "disabled-in-simulation"}}
    write_json(state.repo_path(repo_id), value)
    return {"simulation": True, "repo_id": repo_id, "onboarded": True,
            "policy": value["policy"], "remote_contacted": False, "repository_modified": False}


def seal_snapshot(directory):
    for path in directory.rglob("*"):
        if path.is_symlink():
            raise Invalid("Unsafe input snapshot.")
        path.chmod(0o700 if path.is_dir() else 0o600)
    directory.chmod(0o700)


def generated_question(job):
    return {"schema_version": 1, "simulation": True, "job_id": job["job_id"],
            "request_id": job["request_id"], "checkpoint": job["checkpoint"],
            "question": "Should the example preserve its public API or change it?",
            "choices": list(CHOICES), "recommendation": "keep-api",
            "reason": "The simulated ticket leaves this decision unresolved."}


def start(state, args):
    policy = state.load_repo(args.repo)
    repo = path_without_links(policy["repo_path"])
    if beneath(state.root, repo):
        raise Invalid("State cannot be stored inside an onboarded repository.")
    base = local_head(repo)
    ticket = ticket_input.ticket_path(args.ticket)
    job_id, request_id = uuid.uuid4().hex, uuid.uuid4().hex
    directory = state.root / "jobs" / job_id
    directory.mkdir(mode=0o700)
    try:
        ticket_input.snapshot(repo, ticket, directory / "input")
        seal_snapshot(directory / "input")
        write_json(directory / "policy.json", policy)
        checkpoint = {"schema_version": 1, "simulation": True, "job_id": job_id,
                      "request_id": request_id, "repo_id": args.repo, "base_commit": base,
                      "policy_sha256": digest(private_file(directory / "policy.json")),
                      "input_sha256": digest(private_file(directory / "input/manifest.json")), "attempt": 1}
        write_json(directory / "checkpoint.json", checkpoint)
        checkpoint_hash = digest(private_file(directory / "checkpoint.json"))
        job = {"schema_version": 1, "simulation": True, "job_id": job_id,
               "repo_id": args.repo, "status": "NEEDS_INPUT", "attempt": 1,
               "request_id": request_id, "checkpoint": checkpoint_hash,
               "input_sha256": checkpoint["input_sha256"], "policy_sha256": checkpoint["policy_sha256"],
               "base_commit": base}
        write_json(directory / "question.json", generated_question(job))
        write_json(directory / "job.json", job)
        state.event(directory, job, "job_started")
        state.event(directory, job, "input_required")
        return public_status(job)
    except BaseException:
        # Keep any partial snapshot private, rather than making failed admission
        # look like a runnable job. No cleanup touches the source repository.
        for path in directory.rglob("*"):
            if not path.is_symlink():
                path.chmod(0o700 if path.is_dir() else 0o600)
        directory.chmod(0o700)
        raise


def verify_checkpoint(state, job):
    directory = state.job_dir(job["job_id"])
    if digest(private_file(directory / "checkpoint.json")) != job["checkpoint"]:
        raise Invalid("Stored checkpoint has changed.")
    checkpoint = read_json(directory / "checkpoint.json")
    required = {"job_id": job["job_id"], "request_id": job["request_id"],
                "repo_id": job["repo_id"], "base_commit": job["base_commit"],
                "input_sha256": job["input_sha256"], "policy_sha256": job["policy_sha256"], "attempt": 1}
    if any(checkpoint.get(key) != value for key, value in required.items()):
        raise Invalid("Checkpoint does not match this job.")
    if digest(private_file(directory / "policy.json")) != job["policy_sha256"]:
        raise Invalid("Admitted repository policy has changed.")
    policy = read_json(directory / "policy.json")
    if digest(private_file(directory / "input/manifest.json")) != job["input_sha256"]:
        raise Invalid("Input manifest has changed.")
    manifest = read_json(directory / "input/manifest.json")
    if manifest.get("version") != 1 or not isinstance(manifest.get("files"), list) or len(manifest["files"]) > ticket_input.MAX_FILES:
        raise Invalid("Invalid input manifest.")
    paths = set()
    for entry in manifest["files"]:
        if not isinstance(entry, dict) or set(entry) != {"path", "size", "sha256"}:
            raise Invalid("Invalid input entry.")
        relative = entry["path"]
        if not isinstance(relative, str) or relative in paths:
            raise Invalid("Invalid input path.")
        content = ticket_input.read_markdown(directory / "input", relative)
        private_file(directory / "input" / relative, ticket_input.MAX_FILE_BYTES)
        if len(content) != entry["size"] or digest(content) != entry["sha256"]:
            raise Invalid("Stored ticket snapshot has changed.")
        paths.add(relative)
    actual = {p.relative_to(directory / "input").as_posix() for p in (directory / "input").rglob("*") if not p.is_dir()}
    if actual != paths | {"manifest.json"} or manifest.get("ticket") not in paths:
        raise Invalid("Unexpected files in the ticket snapshot.")
    if local_head(path_without_links(policy["repo_path"])) != job["base_commit"]:
        raise Invalid("Repository HEAD changed since admission; start a new job.")
    question = read_json(directory / "question.json")
    if question != generated_question(job):
        raise Invalid("Question does not match this checkpoint.")
    return directory


def public_status(job):
    return {"simulation": True, "job_id": job["job_id"], "status": job["status"],
            "attempt": job["attempt"], "request_id": job["request_id"],
            "checkpoint": job["checkpoint"], "real_work_executed": False, "publication": "disabled"}


def answer(state, args):
    job = state.load_job(args.job)
    if job["status"] != "NEEDS_INPUT":
        raise Invalid("This job has no unanswered request; replayed answers are rejected.")
    identifier(args.request)
    identifier(args.checkpoint, SHA)
    if args.request != job["request_id"] or args.checkpoint != job["checkpoint"]:
        raise Invalid("Answer references a stale or different request or checkpoint.")
    if args.choice not in CHOICES:
        raise Invalid("Only the recorded enum choices may be admitted.")
    directory = verify_checkpoint(state, job)
    value = {"schema_version": 1, "simulation": True, "job_id": args.job,
             "request_id": args.request, "checkpoint": args.checkpoint,
             "choice": args.choice, "submitted_by": "local-cli-account", "answer_id": uuid.uuid4().hex}
    write_json(directory / "answer.json", value)
    job["status"] = "READY_TO_CONTINUE"
    job["answer_sha256"] = digest(private_file(directory / "answer.json"))
    write_json(directory / "job.json", job, replace=True)
    state.event(directory, job, "answer_admitted")
    return public_status(job)


def continue_job(state, args):
    job = state.load_job(args.job)
    if job["status"] != "READY_TO_CONTINUE":
        raise Invalid("An admitted answer is required and completed jobs cannot continue.")
    directory = verify_checkpoint(state, job)
    if digest(private_file(directory / "answer.json")) != job.get("answer_sha256"):
        raise Invalid("Admitted answer has changed.")
    admitted = read_json(directory / "answer.json")
    if (admitted.get("job_id") != args.job or admitted.get("request_id") != job["request_id"]
            or admitted.get("checkpoint") != job["checkpoint"] or admitted.get("choice") not in CHOICES):
        raise Invalid("Admitted answer does not match this checkpoint.")
    job["attempt"] = 2
    job["status"] = "SIMULATED_COMPLETE"
    result = {"schema_version": 1, "simulation": True, "job_id": args.job,
              "status": "SIMULATED_COMPLETE", "attempt": 2, "choice": admitted["choice"],
              "implementation": "simulated", "review": "simulated", "checks": "not-run",
              "commits": "not-created", "push": "not-performed", "pull_request": "not-created"}
    write_json(directory / "result.json", result)
    write_json(directory / "job.json", job, replace=True)
    state.event(directory, job, "job_continued")
    state.event(directory, job, "simulation_completed")
    return {**public_status(job), "result": result}


class Parser(argparse.ArgumentParser):
    def error(self, message):
        raise Invalid("Invalid command or argument; use --help.")


def parser():
    result = Parser(description=__doc__)
    result.add_argument("--state-dir", required=True, help="Absolute private state directory outside all source repositories.")
    top = result.add_subparsers(dest="group", required=True, parser_class=Parser)
    repos = top.add_parser("repo", help="Admit explicit repository policy without changing the repository.")
    commands = repos.add_subparsers(dest="action", required=True, parser_class=Parser)
    add = commands.add_parser("add")
    add.add_argument("--id", required=True)
    add.add_argument("--path", required=True)
    add.add_argument("--remote", required=True, help="Descriptive OWNER/REPO; never contacted.")
    add.add_argument("--runtime", required=True, choices=RUNTIMES)
    add.add_argument("--implementer", required=True, choices=PROVIDERS)
    add.add_argument("--reviewer", required=True, choices=PROVIDERS)
    add.add_argument("--check", required=True, action="append", help="Descriptive label only; no command executes.")
    commands.add_parser("list")
    jobs = top.add_parser("job", help="Exercise an offline needs-input and explicit-continuation lifecycle.")
    commands = jobs.add_subparsers(dest="action", required=True, parser_class=Parser)
    begin = commands.add_parser("start")
    begin.add_argument("--repo", required=True)
    begin.add_argument("--ticket", required=True)
    for name in ("status", "question", "answer", "continue", "events"):
        sub = commands.add_parser(name)
        sub.add_argument("job")
        if name == "answer":
            sub.add_argument("--request", required=True)
            sub.add_argument("--checkpoint", required=True)
            sub.add_argument("--choice", required=True, choices=CHOICES)
    return result


def execute(args):
    if args.group == "repo" and args.action == "add":
        repo = path_without_links(args.path)
        root = path_without_links(args.state_dir)
        if beneath(root, repo) or beneath(repo, root):
            raise Invalid("Repository and private state must be separate directories.")
    state = State(args.state_dir)
    with state.lock():
        if args.group == "repo":
            if args.action == "add":
                return onboard(state, args)
            entries = []
            for path in sorted((state.root / "repos").glob("*.json")):
                value = read_json(path)
                entries.append({"repo_id": value["repo_id"], "remote": value["remote"], "policy": value["policy"]})
            return {"simulation": True, "repositories": entries}
        if args.action == "start":
            return start(state, args)
        if args.action == "answer":
            return answer(state, args)
        if args.action == "continue":
            return continue_job(state, args)
        job = state.load_job(args.job)
        if args.action == "status":
            return public_status(job)
        if args.action == "question":
            if job["status"] != "NEEDS_INPUT":
                raise Invalid("This job is not waiting for a decision.")
            directory = verify_checkpoint(state, job)
            return read_json(directory / "question.json")
        journal = private_file(state.job_dir(args.job) / "events.jsonl")
        events = [json.loads(line, object_pairs_hook=duplicates) for line in journal.splitlines()]
        return {"simulation": True, "job_id": args.job, "events": events}


def main(argv=None):
    try:
        value = execute(parser().parse_args(argv))
        print(json.dumps(value, sort_keys=True, ensure_ascii=True))
        return 0
    except (Invalid, ValueError, OSError, KeyError, TypeError, subprocess.SubprocessError):
        # Do not echo arbitrary ticket contents, subprocess diagnostics, paths or
        # invalid arguments. Detailed error diagnostics belong in private state.
        print(json.dumps({"simulation": True, "error": "Command rejected; check the lifecycle, identifiers, input integrity and private paths."}), file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
