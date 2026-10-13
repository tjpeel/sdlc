"""Read account capacity through the official client's local control protocol.

No inference, login, credential decoding, custom provider HTTP or quota resets.
Only the explicitly selected numeric fields leave this process.
"""
import datetime
import json
import math
import os
import re
import selectors
import subprocess
import time

MAX_RESPONSE = 256 * 1024
SNAPSHOT_TIMEOUT = 4.0
BUCKET = re.compile(r'[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}\Z')


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat().replace('+00:00', 'Z')


def number(value, maximum):
    return (type(value) in (int, float) and math.isfinite(value)
            and 0 <= value <= maximum)


def codex_windows(result):
    buckets = result.get('rateLimitsByLimitId')
    if buckets is None:
        legacy = result.get('rateLimits')
        buckets = {legacy.get('limitId') or 'codex': legacy} if isinstance(legacy, dict) else {}
    if not isinstance(buckets, dict) or len(buckets) > 16:
        raise ValueError('invalid_response')
    windows = {}
    for identity, bucket in buckets.items():
        if not isinstance(identity, str) or not BUCKET.fullmatch(identity) or not isinstance(bucket, dict):
            continue
        for slot in ('primary', 'secondary'):
            window = bucket.get(slot)
            if not isinstance(window, dict) or not number(window.get('usedPercent'), 100):
                continue
            value = {'remaining_percent': 100 - window['usedPercent']}
            for field, target in (('resetsAt', 'resets_at'), ('windowDurationMins', 'window_duration_mins')):
                if type(window.get(field)) is int and 0 < window[field] <= 1_000_000_000_000:
                    value[target] = window[field]
            windows[identity + '/' + slot] = value
    return windows


def claude_windows(result):
    # The published SDK get_usage response uses 0-100 percentages. The native
    # rate_limit_event stream uses 0-1 ratios and is a separate data source.
    if result.get('rate_limits_available') is not True:
        return {}
    limits = result.get('rate_limits')
    if not isinstance(limits, dict):
        raise ValueError('invalid_response')
    windows = {}
    for name, duration in (('five_hour', 300), ('seven_day', 10080),
                           ('seven_day_opus', 10080), ('seven_day_sonnet', 10080)):
        window = limits.get(name)
        if not isinstance(window, dict) or not number(window.get('utilization'), 100):
            continue
        value = {'remaining_percent': 100 - window['utilization'],
                 'window_duration_mins': duration}
        reset = window.get('resets_at')
        if isinstance(reset, str):
            try:
                stamp = datetime.datetime.fromisoformat(reset.replace('Z', '+00:00'))
                if stamp.tzinfo is not None and 0 < stamp.timestamp() <= 1_000_000_000_000:
                    value['resets_at'] = int(stamp.timestamp())
            except (ValueError, OverflowError):
                pass
        windows['claude/' + name] = value
    return windows


def native_snapshot(env, provider):
    sources = {'codex': 'codex.app_server.rate_limits', 'claude': 'claude.control.get_usage'}
    source = sources[provider]
    snapshot = {'source': source, 'observed_at': now(),
                'status': 'unavailable', 'reason': 'native_error',
                'probe_shutdown': 'not_started'}
    child = None
    try:
        # Run outside the repository and use the worker's guarded native HOME.
        # The account route is direct even when inference uses Headroom.
        native_env = dict(env)
        native_env.pop('OPENAI_BASE_URL', None)
        native_env.pop('ANTHROPIC_BASE_URL', None)
        if provider == 'codex':
            command = ['codex', 'app-server', '-c', 'cli_auth_credentials_store="file"',
                       '-c', 'mcp_servers={}', '-c', 'features.hooks=false', '-c', 'features.plugins=false']
        else:
            command = ['claude', '-p', '--input-format', 'stream-json',
                       '--output-format', 'stream-json', '--verbose',
                       '--no-session-persistence', '--tools', '',
                       '--setting-sources', 'user', '--settings', '{"disableAllHooks":true,"enabledPlugins":{}}',
                       '--strict-mcp-config', '--mcp-config', '{"mcpServers":{}}']
        child = subprocess.Popen(
            command,
            env=native_env, cwd=env['HOME'], stdin=subprocess.PIPE,
            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
        deadline = time.monotonic() + SNAPSHOT_TIMEOUT
        buffer = bytearray()
        received = 0

        def send(message):
            child.stdin.write(json.dumps(message).encode() + b'\n')
            child.stdin.flush()

        with selectors.DefaultSelector() as selector:
            selector.register(child.stdout, selectors.EVENT_READ)

            def response(identifier):
                nonlocal received
                while True:
                    while b'\n' in buffer:
                        line, _, tail = buffer.partition(b'\n')
                        buffer[:] = tail
                        message = json.loads(line)
                        if not isinstance(message, dict):
                            raise ValueError('invalid_response')
                        if provider == 'claude':
                            envelope = message.get('response')
                            if message.get('type') != 'control_response' or not isinstance(envelope, dict) or envelope.get('request_id') != identifier:
                                continue
                            if envelope.get('subtype') != 'success':
                                raise ValueError('native_rejected')
                            result = envelope.get('response')
                            if not isinstance(result, dict):
                                raise ValueError('invalid_response')
                            return result
                        if message.get('id') == identifier:
                            if 'error' in message:
                                error = message.get('error')
                                raise ValueError('unsupported_client' if isinstance(error, dict) and error.get('code') == -32601 else 'native_rejected')
                            result = message.get('result')
                            if not isinstance(result, dict):
                                raise ValueError('invalid_response')
                            return result
                    remaining = deadline - time.monotonic()
                    if remaining <= 0 or not selector.select(remaining):
                        raise ValueError('timeout')
                    data = os.read(child.stdout.fileno(), 4096)
                    if not data:
                        raise ValueError('native_error')
                    received += len(data)
                    if received > MAX_RESPONSE:
                        raise ValueError('invalid_response')
                    buffer.extend(data)

            if provider == 'codex':
                send({'method': 'initialize', 'id': 1,
                      'params': {'clientInfo': {'name': 'sdlc', 'title': 'SDLC quota snapshot', 'version': '1.0.0'}}})
                response(1)
                send({'method': 'initialized', 'params': {}})
                send({'method': 'account/rateLimits/read', 'id': 2})
                windows = codex_windows(response(2))
            else:
                send({'type': 'control_request', 'request_id': 'sdlc-init',
                      'request': {'subtype': 'initialize'}})
                response('sdlc-init')
                send({'type': 'control_request', 'request_id': 'sdlc-usage',
                      'request': {'subtype': 'get_usage'}})
                windows = claude_windows(response('sdlc-usage'))
        snapshot['observed_at'] = now()
        if windows:
            snapshot.update(status='observed', windows=windows)
            snapshot.pop('reason')
        else:
            snapshot['reason'] = 'missing_windows'
    except Exception as error:
        # Never retain native stderr, response errors or account metadata.
        reason = str(error)
        snapshot['reason'] = reason if reason in {'timeout', 'unsupported_client', 'invalid_response', 'native_error', 'native_rejected'} else 'native_error'
    finally:
        if child is not None:
            shutdown = 'natural'
            # End the official client's input stream before sending signals.
            # A completed control probe should be allowed to exit cleanly.
            try:
                child.stdin.close()
            except (BrokenPipeError, OSError):
                pass
            if child.poll() is None:
                try:
                    child.wait(timeout=1)
                except subprocess.TimeoutExpired:
                    shutdown = 'terminated'
                    child.terminate()
                    try:
                        child.wait(timeout=0.25)
                    except subprocess.TimeoutExpired:
                        shutdown = 'killed'
                        child.kill()
            exit_code = child.wait()
            snapshot['probe_shutdown'] = shutdown
            if isinstance(exit_code, int) and not isinstance(exit_code, bool) and -255 <= exit_code <= 255:
                snapshot['probe_exit_code'] = exit_code
            child.stdout.close()
    return snapshot


def codex_snapshot(env):
    return native_snapshot(env, 'codex')


def claude_snapshot(env):
    return native_snapshot(env, 'claude')


def capture(provider, phase, env):
    if provider not in ('codex', 'claude'):
        return
    snapshot = codex_snapshot(env) if provider == 'codex' else claude_snapshot(env)
    print(json.dumps({'type': 'sdlc.subscription_snapshot', 'provider': provider,
                      'phase': phase, 'snapshot': snapshot}), flush=True)


def boundary(provider, phase):
    if provider in ('codex', 'claude'):
        print(json.dumps({'type': 'sdlc.native_work_boundary', 'provider': provider,
                          'phase': phase}), flush=True)
