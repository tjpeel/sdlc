package providerauth

import (
	"os/exec"
	"strconv"
	"testing"
)

func TestNativeResumeModelArgumentsRetainSession(t *testing.T) {
	script := `import ast,os,json,tempfile
from pathlib import Path
source=` + strconv.Quote(headlessHelper) + `
tree=ast.parse(source)
f=next(n for n in tree.body if isinstance(n,ast.FunctionDef) and n.name=='native_command')
ctx={'os':os,'json':json,'SCHEMA':None}
with tempfile.TemporaryDirectory() as d:
 p=Path(d)/'schema.json';p.write_text('{}');ctx['SCHEMA']=p
 exec(compile(ast.Module(body=[f],type_ignores=[]),'headless','exec'),ctx)
 for provider in ('codex','claude'):
  args=ctx['native_command'](provider,'chosen-model','high','existing-session',False)
  assert args[args.index('--model')+1]=='chosen-model',args
  if provider=='codex':
   assert args[1]=='exec' and args[2]=='resume' and args[-2]=='existing-session',args
   assert 'model_reasoning_effort="high"' in args,args
  else:
   assert args[args.index('--resume')+1]=='existing-session' and args[args.index('--effort')+1]=='high',args
`
	out, err := exec.Command("python3", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("offline native argument test: %v\n%s", err, out)
	}
}
