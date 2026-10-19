"""Optional numeric observations of files produced by the pinned official client.

No provider requests, transcript edits, credentials or raw content exports.
Limits affect measurement coverage only, never execution or its budgets.
"""
from datetime import datetime, timezone
import hashlib
import json
from itertools import islice
import os
from pathlib import Path
import stat

LIMIT = 256 * 1024
MAX_FILES = 2048
MAX_SCAN = 4 * 1024 * 1024
MAX_COUNT = 1_000_000_000_000


def emit(value):
    try:
        print(json.dumps(value, separators=(",", ":")), flush=True)
    except (OSError, ValueError):
        pass


def instruction_footprint(provider, skills, agents, instructions, schema, prompt=None):
    """Available manifests and resources; availability is not a model read."""
    metrics = dict(status="complete", skill_files=0, skill_bytes_available=0,
                   agent_files=0, agent_bytes_available=0,
                   shared_instruction_bytes=0, schema_bytes=0, prompt_bytes=0)
    digest = hashlib.sha256()
    scanned = 0
    try:
        for label, root in (("skill", skills), ("agent", agents)):
            root = Path(root)
            if not root.is_dir():
                raise ValueError("missing catalogue")
            paths = sorted(islice(root.rglob("*"), MAX_FILES + 1))
            if len(paths) > MAX_FILES:
                metrics["status"] = "partial"
            for path in paths[:MAX_FILES]:
                path.resolve(strict=True).relative_to(root.resolve(strict=True))
                if not path.is_file():
                    continue
                # Catalogue roots and their internal resource links are pinned,
                # trusted image files. Do not inspect arbitrary workspace files.
                if path.stat().st_size + scanned > MAX_SCAN:
                    metrics["status"] = "partial"
                    continue
                with path.open("rb") as stream:
                    data = stream.read(MAX_SCAN + 1)
                if len(data) + scanned > MAX_SCAN:
                    metrics["status"] = "partial"
                    continue
                scanned += len(data)
                metrics[label + "_files"] += 1
                metrics[label + "_bytes_available"] += len(data)
                digest.update(label.encode() + b"\0" + str(path.relative_to(root)).encode() + b"\0")
                digest.update(hashlib.sha256(data).digest())
        for name, path in (("shared_instruction_bytes", instructions), ("schema_bytes", schema)):
            path = Path(path)
            if path.is_file():
                metrics[name] = path.stat().st_size
        if prompt is not None:
            metrics["prompt_bytes"] = Path(prompt).stat().st_size
        if metrics["status"] == "complete":
            metrics["fingerprint"] = digest.hexdigest()
    except Exception:
        metrics["status"] = "unavailable"
    emit(dict(type="sdlc.instruction_footprint", provider=provider, metrics=metrics))


def count(value):
    return isinstance(value, int) and not isinstance(value, bool) and 0 <= value <= MAX_COUNT


class CodexContext:
    """Invocation-local incremental projection of the root exec rollout.

    Local rollout schema is pinned-client evidence, not a stable exec API.
    Only last request input is reported: it is not the native remaining-context
    percentage, which has different accounting and reserved capacity.
    """
    def __init__(self, config, resume):
        self.root = Path(config) / "sessions"
        self.resume = resume
        self.before = {}
        self.path = None
        self.offset = 0
        self.identity = None
        self.context = None
        self.peak = None
        self.compactions = 0
        self.observations = 0
        self.partial = False
        self.last = None
        self.dropping = False
        self.disabled = False
        try:
            for path in islice(self.root.glob("????/??/??/rollout-*.jsonl"), 65):
                if len(self.before) >= 64:
                    self.partial = True
                    self.disabled = True
                    break
                info = path.lstat()
                self.before[path] = (info.st_ino, info.st_size)
        except Exception:
            self.partial = True
            self.disabled = True

    def select(self):
        if self.disabled:
            return
        candidates = []
        for path in islice(self.root.glob("????/??/??/rollout-*.jsonl"), 65):
            if path.is_symlink():
                self.partial = True
                continue
            old = self.before.get(path)
            if not self.resume and old is not None:
                continue
            fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
            with os.fdopen(fd, "rb") as stream:
                meta = os.fstat(stream.fileno())
                if not stat.S_ISREG(meta.st_mode) or meta.st_uid != os.getuid() or meta.st_nlink != 1:
                    self.partial = True
                    continue
                line = stream.readline(LIMIT + 1)
                if len(line) > LIMIT:
                    self.partial = True
                    continue
                record = json.loads(line)
                if not isinstance(record, dict):
                    self.partial = True
                    continue
                payload = record.get("payload", {})
                if record.get("type") != "session_meta" or not isinstance(payload, dict):
                    continue
                if payload.get("cli_version") != "0.163.0":
                    self.partial = True
                    continue
                session = payload.get("id")
                if self.resume:
                    if session != self.resume:
                        continue
                elif payload.get("source") != "exec" or payload.get("originator") != "codex_exec":
                    continue
                candidates.append((path, session, old))
        if len(candidates) == 1:
            self.path, _, old = candidates[0]
            info = self.path.lstat()
            self.identity = (info.st_dev, info.st_ino)
            if old:
                if old[0] != info.st_ino or info.st_size < old[1]:
                    self.partial = True
                    self.path = None
                    return
                self.offset = old[1]
        elif len(candidates) > 1:
            self.partial = True

    def poll(self, final=False):
        try:
            self._poll(final)
        except Exception:
            self.partial = True
            self.publish(final)

    def _poll(self, final):
        if self.path is None:
            self.select()
        if self.path is not None:
            fd = os.open(self.path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
            with os.fdopen(fd, "rb") as stream:
                info = os.fstat(stream.fileno())
                if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_nlink != 1 or (info.st_dev, info.st_ino) != self.identity or info.st_size < self.offset:
                    self.partial = True
                    self.publish(final)
                    return
                stream.seek(self.offset)
                scanned = 0
                while scanned < MAX_SCAN:
                    start = stream.tell()
                    line = stream.readline(LIMIT + 1)
                    if not line:
                        break
                    scanned += len(line)
                    if self.dropping:
                        self.offset = stream.tell()
                        self.dropping = not line.endswith(b"\n")
                        continue
                    if len(line) > LIMIT:
                        self.partial = True
                        self.dropping = not line.endswith(b"\n")
                        self.offset = stream.tell()
                        continue
                    if not line.endswith(b"\n"):
                        # Keep an incomplete append for the next poll.
                        if final:
                            self.partial = True
                        stream.seek(start)
                        break
                    self.offset = stream.tell()
                    try:
                        record = json.loads(line)
                    except (ValueError, UnicodeError):
                        self.partial = True
                        continue
                    self.observe(record)
                if final and self.offset < info.st_size:
                    self.partial = True
        self.publish(final)

    def observe(self, record):
        if not isinstance(record, dict):
            self.partial = True
            return
        if record.get("type") == "compacted":
            self.compactions += 1
        payload = record.get("payload")
        if record.get("type") != "event_msg" or not isinstance(payload, dict) or payload.get("type") != "token_count":
            return
        info = payload.get("info")
        if not isinstance(info, dict):
            return
        usage = info.get("last_token_usage")
        if not isinstance(usage, dict) or not count(usage.get("input_tokens")):
            self.partial = True
            return
        value = usage["input_tokens"]
        context = dict(source="codex.rollout.last_token_usage", scope="request_context",
                       input_tokens=value, tokens=value,
                       observed_at=datetime.now(timezone.utc).isoformat())
        cached = usage.get("cached_input_tokens")
        if count(cached) and cached <= value:
            context["cached_input_tokens"] = cached
        window = info.get("model_context_window")
        if count(window) and window > 0:
            context["window"] = window
        self.context = context
        self.peak = max(self.peak or 0, value)
        self.observations += 1

    def publish(self, final):
        coverage = "complete" if final and self.path is not None and not self.partial else "partial"
        if self.path is None:
            coverage = "unavailable"
        event = dict(type="sdlc.codex_context", provider="codex", coverage=coverage,
                     compactions=self.compactions, observations=self.observations)
        if self.context is not None:
            event.update(context=self.context, peak_input_tokens=self.peak)
        signature = json.dumps(event, sort_keys=True)
        if final or signature != self.last:
            emit(event)
            self.last = signature
