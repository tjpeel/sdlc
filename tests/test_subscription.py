"""Offline official-client protocol boundary checks; no provider credentials."""
import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
import subprocess
import unittest
from unittest.mock import Mock, patch

SOURCE = Path(__file__).resolve().parents[1] / 'internal/providerauth/subscription.py'
spec = importlib.util.spec_from_file_location('subscription_test', SOURCE)
subscription = importlib.util.module_from_spec(spec)
spec.loader.exec_module(subscription)


class SubscriptionTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        client = self.root / 'codex'
        client.write_text('#!' + sys.executable + '\n' + '''
import json,os,sys,time
assert sys.argv[1]=='app-server'
for line in sys.stdin:
    message=json.loads(line)
    method=message['method']
    assert method in {'initialize','initialized','account/rateLimits/read'}
    with open(os.environ['PROTOCOL_LOG'],'a') as log:log.write(method+'\\n')
    if method=='initialized':continue
    if method=='initialize':
        print(json.dumps({'id':message['id'],'result':{'userAgent':'private metadata'}}),flush=True)
        continue
    mode=os.environ.get('PROTOCOL_MODE','observed')
    if mode=='timeout':
        sys.stdout.write('{');sys.stdout.flush();time.sleep(30)
    if mode in ('unsupported','error'):
        print(json.dumps({'id':message['id'],'error':{'code':-32601 if mode=='unsupported' else 401,'message':'fake-private-error'}}),flush=True)
    else:
        result={'rateLimitsByLimitId':{'codex':{'primary':{'usedPercent':25,'windowDurationMins':300,'resetsAt':4102444800},'secondary':{'usedPercent':70,'windowDurationMins':10080,'resetsAt':4102444900},'email':'private@example.invalid','credits':{'balance':'private'}},'other':{'primary':{'usedPercent':150}}},'account':'private@example.invalid'}
        if mode=='empty':result={'rateLimits':None,'rateLimitsByLimitId':None}
        print(json.dumps({'id':message['id'],'result':result}),flush=True)
''')
        client.chmod(0o700)
        self.env = {'PATH': str(self.root) + os.pathsep + os.defpath,
                    'HOME': str(self.root), 'PROTOCOL_LOG': str(self.root/'methods')}

    def test_native_control_only_and_numeric_projection(self):
        result = subscription.codex_snapshot(self.env)
        self.assertEqual(result['status'], 'observed')
        self.assertEqual(result['probe_shutdown'], 'natural')
        self.assertEqual(result['probe_exit_code'], 0)
        self.assertEqual(result['windows']['codex/primary']['remaining_percent'], 75)
        self.assertEqual(result['windows']['codex/secondary']['remaining_percent'], 30)
        self.assertEqual(set(result['windows']), {'codex/primary','codex/secondary'})
        self.assertNotIn('private', json.dumps(result))
        self.assertEqual((self.root/'methods').read_text().splitlines(),
                         ['initialize','initialized','account/rateLimits/read'])

    def test_unavailable_never_invents_full_allowance_or_leaks_errors(self):
        for mode, reason in [('unsupported','unsupported_client'),('error','native_rejected'),('empty','missing_windows')]:
            with self.subTest(mode=mode):
                result = subscription.codex_snapshot(dict(self.env, PROTOCOL_MODE=mode))
                self.assertEqual(result['status'], 'unavailable')
                self.assertEqual(result['reason'], reason)
                self.assertNotIn('windows', result)
                self.assertNotIn('fake-private-error', json.dumps(result))

    def test_partial_response_is_bounded_and_reaps_native_client(self):
        with patch.object(subscription, 'SNAPSHOT_TIMEOUT', .1):
            result = subscription.codex_snapshot(dict(self.env, PROTOCOL_MODE='timeout'))
        self.assertEqual(result['reason'], 'timeout')

    def test_probe_shutdown_records_only_fixed_lifecycle_metadata(self):
        for mode, polled, waits, code in [
            ('natural', 0, [0], 0),
            ('natural', None, [0, 0], 0),
            ('terminated', None, [subprocess.TimeoutExpired('fake-client', 1), -15, -15], -15),
            ('killed', None, [subprocess.TimeoutExpired('fake-client', 1), subprocess.TimeoutExpired('fake-client', .25), -9], -9),
        ]:
            with self.subTest(mode=mode):
                child = Mock()
                child.poll.return_value = polled
                child.wait.side_effect = waits
                selector = Mock()
                selector.select.return_value = []
                context = Mock()
                context.__enter__ = Mock(return_value=selector)
                context.__exit__ = Mock(return_value=False)
                with patch.object(subscription.subprocess, 'Popen', return_value=child), patch.object(subscription.selectors, 'DefaultSelector', return_value=context):
                    result = subscription.claude_snapshot(self.env)
                self.assertEqual(result['reason'], 'timeout')
                self.assertEqual(result['probe_shutdown'], mode)
                self.assertEqual(result['probe_exit_code'], code)
                self.assertEqual(child.terminate.call_count, int(mode != 'natural'))
                self.assertEqual(child.kill.call_count, int(mode == 'killed'))
                child.stdin.close.assert_called_once()
                child.stdout.close.assert_called_once()
        with patch.object(subscription.subprocess, 'Popen', side_effect=OSError('fake-private-error')):
            result = subscription.claude_snapshot(self.env)
        self.assertEqual(result['probe_shutdown'], 'not_started')
        self.assertNotIn('probe_exit_code', result)
        self.assertNotIn('fake-private-error', json.dumps(result))

    def test_legacy_windows_and_missing_percentages(self):
        result = subscription.codex_windows({'rateLimits':{'primary':{'usedPercent':0},'secondary':{'usedPercent':None}}})
        self.assertEqual(result, {'codex/primary': {'remaining_percent':100}})
        self.assertEqual(subscription.codex_windows({'rateLimits':{'primary':{'usedPercent':True}}}), {})

    def claude_client(self):
        client = self.root / 'claude'
        client.write_text('#!' + sys.executable + '\n' + '''
import json,os,sys
assert '--input-format' in sys.argv and '--no-session-persistence' in sys.argv
for line in sys.stdin:
    message=json.loads(line)
    assert message['type']=='control_request'
    request=message['request']['subtype']
    assert request in {'initialize','get_usage'}
    with open(os.environ['PROTOCOL_LOG'],'a') as log:log.write(request+'\\n')
    response={}
    mode=os.environ.get('PROTOCOL_MODE','observed')
    if request=='get_usage':
        response={'rate_limits_available':True,'rate_limits':{'five_hour':{'utilization':25,'resets_at':'2100-01-01T00:00:00Z'},'seven_day':{'utilization':70,'resets_at':'2100-01-02T00:00:00Z'}},'subscription_type':'private-plan','behaviors':{'private':'discard'},'session':{'total_cost_usd':42}}
        if mode=='empty':response={'rate_limits_available':False,'rate_limits':None}
        if mode=='changed':response={'rate_limits_available':True,'rate_limits':[]}
    envelope={'subtype':'success','request_id':message['request_id'],'response':response}
    if mode=='error' and request=='get_usage':envelope={'subtype':'error','request_id':message['request_id'],'error':'fake-private-error'}
    print(json.dumps({'type':'control_response','response':envelope}),flush=True)
''')
        client.chmod(0o700)

    def test_claude_control_only_percentages_and_iso_reset(self):
        self.claude_client()
        result = subscription.claude_snapshot(self.env)
        self.assertEqual(result['status'], 'observed')
        self.assertEqual(result['windows']['claude/five_hour']['remaining_percent'], 75)
        self.assertEqual(result['windows']['claude/five_hour']['resets_at'], 4102444800)
        self.assertEqual(result['windows']['claude/seven_day']['remaining_percent'], 30)
        self.assertNotIn('private', json.dumps(result))
        self.assertNotIn('total_cost', json.dumps(result))
        self.assertEqual((self.root/'methods').read_text().splitlines(), ['initialize','get_usage'])

    def test_claude_unavailable_or_changed_native_schema(self):
        self.claude_client()
        for mode, reason in [('empty','missing_windows'),('error','native_rejected'),('changed','invalid_response')]:
            with self.subTest(mode=mode):
                result = subscription.claude_snapshot(dict(self.env, PROTOCOL_MODE=mode))
                self.assertEqual(result['status'], 'unavailable')
                self.assertEqual(result['reason'], reason)
                self.assertNotIn('windows', result)
                self.assertNotIn('fake-private-error', json.dumps(result))

    def test_claude_get_usage_units_differ_from_native_event_ratios(self):
        result = subscription.claude_windows({'rate_limits_available':True,'rate_limits':{
            'five_hour':{'utilization':.7,'resets_at':None},
            'seven_day':{'utilization':None},'seven_day_opus':{'utilization':100}}})
        self.assertAlmostEqual(result['claude/five_hour']['remaining_percent'], 99.3)
        self.assertNotIn('resets_at', result['claude/five_hour'])
        self.assertNotIn('claude/seven_day', result)
        self.assertEqual(result['claude/seven_day_opus']['remaining_percent'], 0)


if __name__ == '__main__':
    unittest.main()
