"""Fresh, non-interactive agent sessions with private results and strict output checks."""
import json
import math
import os
from pathlib import Path
import signal
import subprocess
import threading
import unicodedata

WORKSPACE = Path('/workspace/repo')
MAX_RESULT_BYTES = 16 * 1024 * 1024
SCHEMAS = {
    'implementation': {
        'type': 'object',
        'properties': {
            'status': {'type': 'string', 'enum': ['complete', 'blocked']},
            'title': {'type': 'string'},
            'body': {'type': 'string'},
        },
        'required': ['status', 'title', 'body'],
        'additionalProperties': False,
    },
    'review': {
        'type': 'object',
        'properties': {
            'verdict': {'type': 'string', 'enum': ['pass', 'changes_requested']},
            'findings': {'type': 'array', 'items': {'type': 'string'}},
        },
        'required': ['verdict', 'findings'],
        'additionalProperties': False,
    },
}


class AgentError(RuntimeError):
    """An error safe to report without exposing agent diagnostics or credentials."""


def _model_name(model):
    if model is None or model == '':
        return None
    if (not isinstance(model, str) or model.startswith('-')
            or any(c.isspace() or unicodedata.category(c).startswith('C') for c in model)):
        raise AgentError('Model must be a name without whitespace, controls or a leading dash.')
    return model


def _private_file(path):
    """Create an artifact once, refusing existing files and symlinks."""
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    return os.fdopen(descriptor, 'wb')


def _write_json(path, value):
    with _private_file(path) as stream:
        stream.write((json.dumps(value, indent=2, ensure_ascii=False) + '\n').encode('utf-8'))


def _read_json(path):
    try:
        if path.is_symlink() or not path.is_file() or path.stat().st_size > MAX_RESULT_BYTES:
            raise AgentError('Agent did not return a readable structured result.')
        return json.loads(path.read_text(encoding='utf-8'))
    except (OSError, UnicodeError, ValueError):
        raise AgentError('Agent did not return valid structured JSON.') from None


def _validate_result(value, role):
    schema = SCHEMAS[role]
    if not isinstance(value, dict) or set(value) != set(schema['required']):
        raise AgentError('Agent structured result has unexpected or missing fields.')
    if role == 'implementation':
        if value['status'] not in ('complete', 'blocked'):
            raise AgentError('Agent returned an unknown implementation status.')
        if any(not isinstance(value[key], str) or not value[key].strip()
               for key in ('title', 'body')):
            raise AgentError('Agent implementation title and body must be nonempty strings.')
    else:
        if value['verdict'] not in ('pass', 'changes_requested'):
            raise AgentError('Agent returned an unknown review verdict.')
        findings = value['findings']
        if not isinstance(findings, list) or any(
                not isinstance(item, str) or not item.strip() for item in findings):
            raise AgentError('Agent review findings must be an array of nonempty strings.')
        if value['verdict'] == 'pass' and findings:
            raise AgentError('Agent returned a passing review with unresolved findings.')
    return value


def _terminate_group(process):
    # The CLI and its test/build tools share a new process group. Kill remaining
    # descendants even if the CLI exits before them or they ignore SIGTERM.
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        pass
    try:
        process.wait(timeout=1)
    except subprocess.TimeoutExpired:
        pass
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    process.wait()
    if process.stdin is not None:
        process.stdin.close()


def _interrupted(signum, frame):
    raise AgentError('Agent session was interrupted.')


def run_agent(provider, model, prompt, results_dir, role, timeout_seconds=3600):
    """Run an agent, returning validated schema fields plus provider/model/role.

    The caller supplies a separate directory for each attempt, outside the
    checkout. Raw events and stderr stay there; errors never include CLI output.
    Container isolation must already exist before this function is called.
    """
    if not isinstance(provider, str) or provider not in ('codex', 'claude'):
        raise AgentError('Provider must be codex or claude.')
    if not isinstance(role, str) or role not in SCHEMAS:
        raise AgentError('Agent role must be implementation or review.')
    model = _model_name(model)
    if not isinstance(prompt, str) or not prompt.strip():
        raise AgentError('Agent prompt must be a nonempty string.')
    if (isinstance(timeout_seconds, bool) or not isinstance(timeout_seconds, (int, float))
            or not math.isfinite(timeout_seconds) or timeout_seconds <= 0):
        raise AgentError('Agent timeout must be a positive finite number of seconds.')

    try:
        results = Path(results_dir).resolve()
        workspace = WORKSPACE.resolve()
        if results == workspace or workspace in results.parents:
            raise AgentError('Agent results must be saved outside the repository checkout.')
        results.mkdir(parents=True, mode=0o700, exist_ok=True)
        results.chmod(0o700)
        schema_path = results / 'schema.json'
        raw_path = results / 'agent-result.json'
        _write_json(schema_path, SCHEMAS[role])
        if provider == 'codex':
            # Precreate the output file with private permissions; Codex writes
            # its final JSON to this file rather than relying on event parsing.
            with _private_file(raw_path):
                pass
            command = ['codex', 'exec', '--dangerously-bypass-approvals-and-sandbox',
                       '--json', '--output-schema', str(schema_path),
                       '--output-last-message', str(raw_path), '-C', str(WORKSPACE)]
            event_path = results / 'events.jsonl'
        else:
            command = ['claude', '-p', '--dangerously-skip-permissions',
                       '--output-format', 'json', '--json-schema',
                       json.dumps(SCHEMAS[role], separators=(',', ':'))]
            if role == 'review':
                # --allowedTools only grants permissions; --tools removes the
                # mutating built-ins. MCP tools need their own deny rule.
                command += ['--tools', 'Read,Glob,Grep,Skill', '--disallowedTools', 'mcp__*']
            event_path = results / 'events.json'
        if model is not None:
            command += ['--model', model]
        with _private_file(event_path) as events, _private_file(results / 'stderr.log') as stderr:
            handlers = {}
            process = None
            try:
                if threading.current_thread() is threading.main_thread():
                    for number in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
                        handlers[number] = signal.signal(number, _interrupted)
                try:
                    process = subprocess.Popen(command, cwd=WORKSPACE, stdin=subprocess.PIPE,
                                               stdout=events, stderr=stderr, text=True,
                                               encoding='utf-8', start_new_session=True)
                except OSError:
                    raise AgentError(f'{provider} CLI could not start.') from None
                try:
                    process.communicate(input=prompt, timeout=timeout_seconds)
                except subprocess.TimeoutExpired:
                    raise AgentError('Agent session exceeded its time limit.') from None
                if process.returncode:
                    raise AgentError(f'{provider} CLI failed; inspect the private diagnostics.')
            finally:
                # Also clean up background children left by a successful CLI.
                for number in handlers:
                    signal.signal(number, signal.SIG_IGN)
                try:
                    if process is not None:
                        _terminate_group(process)
                finally:
                    for number, previous in handlers.items():
                        signal.signal(number, previous)
        if provider == 'claude':
            envelope = _read_json(event_path)
            if (not isinstance(envelope, dict) or envelope.get('type') != 'result'
                    or envelope.get('subtype') != 'success' or envelope.get('is_error') is not False
                    or 'structured_output' not in envelope):
                raise AgentError('Claude did not return a successful structured result.')
            value = envelope['structured_output']
            _write_json(raw_path, value)
        else:
            if raw_path.is_symlink():
                raise AgentError('Agent structured result must be a regular file.')
            raw_path.chmod(0o600)
            value = _read_json(raw_path)
        result = {'provider': provider, 'model': model, 'role': role,
                  **_validate_result(value, role)}
        _write_json(results / 'result.json', result)
        return result
    except OSError:
        raise AgentError('Unable to prepare or save private agent results.') from None
