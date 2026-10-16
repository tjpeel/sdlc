package providerauth

import (
	"os/exec"
	"strconv"
	"testing"
)

func TestCodexInterruptedHistoryUsesOfficialSameThreadCompaction(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python unavailable")
	}
	script := `import ast,json,os,sys,tempfile,subprocess,types,contextlib,io
from pathlib import Path
source=` + strconv.Quote(headlessHelper) + `
tree=ast.parse(source)
functions=[n for n in tree.body if isinstance(n,ast.FunctionDef) and n.name in ('unmatched_codex_calls','compact_interrupted_codex','apply_headroom_route')]
exec(compile(ast.Module(body=functions,type_ignores=[]),'headless','exec'))
with tempfile.TemporaryDirectory() as temp:
 root=Path(temp);native=types.SimpleNamespace(WORKSPACE=str(root))
 sid='00000000-0000-4000-8000-000000000001'
 config=root/'codex';folder=config/'sessions/2026/10/16';folder.mkdir(parents=True)
 history=folder/('rollout-2026-10-16T00-00-00-'+sid+'.jsonl')
 original=json.dumps({'type':'session_meta','payload':{'id':sid}})+'\n'+json.dumps({'type':'response_item','payload':{'type':'custom_tool_call','call_id':'interrupted','name':'exec','input':'private input'}})+'\n'
 history.write_text(original)
 assert unmatched_codex_calls(config,sid)
 binary=root/'codex-cli';binary.mkdir()
 fake=binary/'codex'
 fake.write_text('#!'+sys.executable+'\n'+'''import sys,json,os
from pathlib import Path
for line in sys.stdin:
 message=json.loads(line);method=message['method']
 with open(os.environ['RPC_LOG'],'a') as f:f.write(json.dumps({'method':method,'params':message.get('params'),'argv':sys.argv[1:],'base':os.environ.get('OPENAI_BASE_URL')})+'\\n')
 if method=='initialized':continue
 result={}
 if method=='thread/resume':result={'thread':{'id':os.environ['THREAD_ID'],'status':{'type':'active' if os.environ.get('FAILURE')=='active' else 'idle'}}}
 if method=='thread/compact/start':
  if os.environ.get('FAILURE')=='rpc':
   print(json.dumps({'id':message['id'],'error':{'code':-1,'message':'private native failure'}}),flush=True);continue
  with open(os.environ['HISTORY'],'a') as f:f.write(json.dumps({'type':'compacted','payload':{'message':'native summary'}})+'\\n')
  print(json.dumps({'method':'turn/completed','params':{'threadId':os.environ['THREAD_ID'],'turn':{'status':'completed'}}}),flush=True)
 print(json.dumps({'id':message['id'],'result':result}),flush=True)
''')
 fake.chmod(0o755)
 env=dict(os.environ,PATH=str(binary)+os.pathsep+os.environ['PATH'],THREAD_ID=sid,HISTORY=str(history),RPC_LOG=str(root/'rpc.log'))
 os.environ['SDLC_HEADROOM']='1';os.environ['SDLC_HEADROOM_URL']='http://host.docker.internal:8787'
 with contextlib.redirect_stdout(io.StringIO()) as output:
  compact_interrupted_codex(config,sid,'selected-model','high',env)
 assert not unmatched_codex_calls(config,sid)
 assert history.read_text().startswith(original)
 assert 'custom_tool_call_output' not in history.read_text()
 calls=[json.loads(x) for x in (root/'rpc.log').read_text().splitlines()]
 assert [c['method'] for c in calls]==['initialize','initialized','thread/resume','thread/compact/start']
 assert calls[-1]['params']=={'threadId':sid}
 assert calls[-1]['base']=='http://host.docker.internal:8787/v1'
 assert 'openai_base_url="http://host.docker.internal:8787/v1"' in calls[-1]['argv']
 assert 'model="selected-model"' in calls[-1]['argv']
 assert 'model_reasoning_effort="high"' in calls[-1]['argv']
 events=[json.loads(x) for x in output.getvalue().splitlines()]
 assert [e['phase'] for e in events]==['started','completed']
 compact_interrupted_codex(config,sid,'selected-model','high',env)
 assert len((root/'rpc.log').read_text().splitlines())==4
 for failure in ('active','rpc'):
  history.write_text(original);(root/'rpc.log').unlink();env['FAILURE']=failure
  try:
   with contextlib.redirect_stdout(io.StringIO()):compact_interrupted_codex(config,sid,'selected-model','high',env)
  except ValueError:pass
  else:raise AssertionError('unsafe or rejected recovery succeeded')
  assert history.read_text()==original and unmatched_codex_calls(config,sid)
`
	if output, err := exec.Command("python3", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("native recovery: %v\n%s", err, output)
	}
}
