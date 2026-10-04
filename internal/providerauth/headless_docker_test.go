package providerauth

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

// Native startup reports the available profiles even without login. No auth
// volume, signing state or network is available to this opt-in regression.
func TestOfflineClaudePinnedAgentDiscovery(t *testing.T) {
	if os.Getenv("SDLC_OFFLINE_DOCKER_TESTS") != "1" {
		t.Skip("set SDLC_OFFLINE_DOCKER_TESTS=1 to check native Claude catalogue discovery")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runtime, err := runtimeimage.New(io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	state, err := runtime.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	auth, _ := json.Marshal(helper)
	headless, _ := json.Marshal(headlessHelper)
	script := "import json,types\n_auth={'__name__':'offline_auth'}\nexec(" + string(auth) + ",_auth)\n_headless={'__name__':'offline_headless','native':types.SimpleNamespace(**_auth)}\nexec(" + string(headless) + ",_headless)\n" + `
import os,pathlib,subprocess
native=_headless['native']
config=pathlib.Path('/tmp/config');config.mkdir(mode=0o700)
home=pathlib.Path('/tmp/home');home.mkdir(mode=0o700)
schema=pathlib.Path('/tmp/schema.json');schema.write_text('{}')
_headless['SCHEMA']=schema
native.claude_catalogues(config)
env=native.interactive_environment(str(home))
env.update({'CLAUDE_CONFIG_DIR':str(config),'DISABLE_AUTOUPDATER':'1','CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC':'1','CLAUDE_CODE_DISABLE_AUTO_MEMORY':'1','ENABLE_CLAUDEAI_MCP_SERVERS':'false'})
command=_headless['native_command']('claude','claude-opus-5-5','high','',True)
result=subprocess.run(command,input='Offline catalogue validation only.',env=env,capture_output=True,text=True,timeout=12)
events=[json.loads(line) for line in result.stdout.splitlines() if line.startswith('{')]
startup=next(event for event in events if event.get('type')=='system' and event.get('subtype')=='init')
required={'read_low','read_medium','read_high','read_exceptional','write_medium'}
assert required <= set(startup['agents']),'pinned profiles missing from native discovery'
assert startup['mcp_servers']==[],'unexpected MCP server'
assert startup.get('apiKeySource')=='none','unexpected authentication'
assert result.returncode != 0,'unauthenticated probe unexpectedly connected'
print('Pinned Claude agents discovered without credentials or network')
`
	output, err := runtime.Docker.Output(ctx, "run", "--rm", "--pull", "never", "--network", "none", "--user", "1000:1000", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "128", "--memory", "512m", "--log-driver", "none", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=64m,mode=1777", "--entrypoint", "python3", state.ImageID, "-c", script)
	if err != nil {
		t.Fatal("offline native Claude agent discovery failed")
	}
	t.Log(string(output))
}
