package providerauth

import (
	"os/exec"
	"strconv"
	"testing"
)

func TestNumericNativeTelemetry(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("Python is required for native telemetry verification")
	}
	script := `import ast,contextlib,io,json,os,signal,subprocess,sys,tempfile,types,unittest
from pathlib import Path
exec(` + strconv.Quote(telemetryHelper) + `)
headless_source=` + strconv.Quote(headlessHelper) + `

def record(kind,payload):
 return json.dumps(dict(type=kind,payload=payload))+'\n'
def meta(sid='root',source='exec',version='0.163.0'):
 return record('session_meta',dict(id=sid,source=source,originator='codex_exec',cli_version=version))
def sample(value,window=1000):
 return record('event_msg',dict(type='token_count',info=dict(last_token_usage=dict(input_tokens=value,cached_input_tokens=2),model_context_window=window,total_token_usage=dict(input_tokens=999999))))
def poll(monitor,final=False):
 out=io.StringIO()
 with contextlib.redirect_stdout(out):monitor.poll(final)
 return [json.loads(line) for line in out.getvalue().splitlines()]

class Checks(unittest.TestCase):
 def setUp(self):
  self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup)
  self.root=Path(self.temp.name);self.folder=self.root/'sessions/2026/10/19';self.folder.mkdir(parents=True)
  self.path=self.folder/'rollout-root.jsonl'
 def test_fresh_root_excludes_child_and_exports_no_text(self):
  m=CodexContext(self.root,'')
  self.path.write_text(meta()+sample(300)+sample(200)+record('compacted',dict(message='private compaction')))
  (self.folder/'rollout-child.jsonl').write_text(meta('child',dict(subagent='root'))+sample(900))
  e=poll(m,True)[-1]
  self.assertEqual(e['coverage'],'complete');self.assertEqual(e['peak_input_tokens'],300)
  self.assertEqual(e['context']['tokens'],200);self.assertEqual(e['context']['window'],1000)
  self.assertEqual(e['compactions'],1);self.assertEqual(e['observations'],2)
  self.assertNotIn('private',json.dumps(e));self.assertNotIn(str(self.root),json.dumps(e));self.assertNotIn('999999',json.dumps(e))
 def test_resume_reads_only_appended_records_and_split_lines(self):
  self.path.write_text(meta()+sample(800)+record('compacted',{}))
  m=CodexContext(self.root,'root')
  with self.path.open('a') as f:f.write(sample(100)[:-3])
  poll(m);self.assertIsNone(m.context)
  with self.path.open('a') as f:f.write(sample(100)[-3:]+record('compacted',{}))
  e=poll(m,True)[-1]
  self.assertEqual(e['peak_input_tokens'],100);self.assertEqual(e['compactions'],1);self.assertEqual(e['observations'],1)
 def test_ambiguity_unsupported_and_symlink_are_unknown(self):
  for version in ('0.1.0','0.163.0'):
   m=CodexContext(self.root,'')
   self.path.write_text(meta(version=version)+sample(300))
   if version=='0.163.0':(self.folder/'rollout-other.jsonl').write_text(meta('other')+sample(900))
   e=poll(m,True)[-1];self.assertEqual(e['coverage'],'unavailable');self.assertNotIn('context',e)
   self.path.unlink()
  (self.folder/'rollout-other.jsonl').unlink()
  target=self.root/'private';target.write_text(meta()+sample(700))
  self.path.symlink_to(target);e=poll(CodexContext(self.root,''),True)[-1]
  self.assertEqual(e['coverage'],'unavailable')
 def test_malformed_oversized_and_replacement_leave_partial(self):
  m=CodexContext(self.root,'');self.path.write_text(meta()+sample(True)+('x'*(LIMIT+10))+'\n'+sample(120))
  e=poll(m)[-1];self.assertEqual(e['context']['tokens'],120);self.assertEqual(e['coverage'],'partial')
  replacement=self.folder/'new';replacement.write_text(meta()+sample(5));os.replace(replacement,self.path)
  e=poll(m,True)[-1];self.assertEqual(e['coverage'],'partial');self.assertEqual(e['context']['tokens'],120)
 def test_overflow_baseline_never_replays_existing_or_new_files(self):
  for index in range(65):
   (self.folder/('rollout-'+str(index)+'.jsonl')).write_text(meta(str(index))+sample(800))
  m=CodexContext(self.root,'')
  self.path.write_text(meta()+sample(100))
  e=poll(m,True)[-1]
  self.assertEqual(e['coverage'],'unavailable');self.assertNotIn('context',e);self.assertEqual(e['compactions'],0)
 def test_nonzero_native_exit_and_input_are_preserved(self):
  functions=[n for n in ast.parse(headless_source).body if isinstance(n,ast.FunctionDef) and n.name=='process']
  namespace=dict(signal=signal,subprocess=subprocess,native=types.SimpleNamespace(WORKSPACE=self.root),PROMPT=self.root/'prompt')
  namespace['PROMPT'].write_text('unchanged input')
  exec(compile(ast.Module(body=functions,type_ignores=[]),'headless','exec'),namespace)
  m=CodexContext(self.root,'')
  script="import sys;from pathlib import Path;assert sys.stdin.read()=='unchanged input';Path('sessions/2026/10/19/rollout-root.jsonl').write_text("+repr(meta()+sample(110))+");sys.exit(7)"
  output=io.StringIO()
  with contextlib.redirect_stdout(output):code=namespace['process']([sys.executable,'-c',script],dict(os.environ),m)
  self.assertEqual(code,7);self.assertEqual(m.context['input_tokens'],110)
  self.assertEqual(json.loads(output.getvalue())['coverage'],'complete')
 def test_catalogue_counts_utf8_without_exporting_content(self):
  skills=self.root/'skills';agents=self.root/'agents';skills.mkdir();agents.mkdir()
  (skills/'SKILL.md').write_bytes('abc\u00e9'.encode());(agents/'profile.toml').write_bytes(b'ab')
  schema=self.root/'schema';schema.write_bytes(b'{}')
  out=io.StringIO()
  with contextlib.redirect_stdout(out):instruction_footprint('codex',skills,agents,self.root/'absent',schema)
  e=json.loads(out.getvalue());self.assertEqual(e['metrics']['skill_bytes_available'],5)
  self.assertEqual(e['metrics']['agent_bytes_available'],2);self.assertEqual(e['metrics']['schema_bytes'],2)
  self.assertNotIn('SKILL.md',out.getvalue());self.assertNotIn(str(self.root),out.getvalue())

unittest.main()
`
	if output, err := exec.Command(python, "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("native telemetry: %v\n%s", err, output)
	}
}
