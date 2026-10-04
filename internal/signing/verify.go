package signing

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"time"
)

const verificationSuccess = "signing verification passed\n"

// This trusted helper reads only stdin, writes only its disposable tmpfs, and
// invokes fixed Git/OpenSSH binaries without loading repository or host config.
const verificationHelper = `import os,pathlib,subprocess,sys
root=pathlib.Path('/tmp/sdlc-signing-verification');root.mkdir(mode=0o700)
env={'PATH':'/usr/bin:/bin','HOME':str(root),'LANG':'C.UTF-8','GIT_CONFIG_NOSYSTEM':'1','GIT_CONFIG_GLOBAL':'/dev/null','GIT_TERMINAL_PROMPT':'0','GIT_NO_REPLACE_OBJECTS':'1'}
key=bytearray(sys.stdin.buffer.read(65537))
if not key or len(key)>65536:raise ValueError('invalid signing input')
keypath=root/'key'
fd=os.open(keypath,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
with os.fdopen(fd,'wb') as output:output.write(key)
for index in range(len(key)):key[index]=0
def command(*args,input=None):
 return subprocess.check_output(args,input=input,env=env,stderr=subprocess.DEVNULL)
public=' '.join(command('/usr/bin/ssh-keygen','-y','-P','','-f',str(keypath)).decode().split()[:2])
if public!=sys.argv[1]:raise ValueError('public identity mismatch')
publicpath=root/'public';publicpath.write_text(public+'\n');publicpath.chmod(0o600)
fingerprint=command('/usr/bin/ssh-keygen','-lf',str(publicpath),'-E','sha256').decode().split()[1]
if fingerprint!=sys.argv[2]:raise ValueError('fingerprint mismatch')
allowed=root/'allowed';allowed.write_text('* '+public+'\n');allowed.chmod(0o600)
repo=root/'repository.git'
command('/usr/bin/git','init','--bare','--template=',str(repo))
git=['/usr/bin/git','-C',str(repo),'-c','core.hooksPath=/dev/null','-c','core.fsmonitor=false','-c','user.name=Disposable Verification','-c','user.email=verification@example.invalid','-c','gpg.format=ssh','-c','gpg.ssh.program=/usr/bin/ssh-keygen','-c','gpg.ssh.allowedSignersFile='+str(allowed),'-c','user.signingKey='+str(keypath)]
tree=command(*git,'hash-object','-t','tree','-w','--stdin',input=b'').decode().strip()
commit=command(*git,'commit-tree',tree,'-S','-m','Disposable signing verification').decode().strip()
command(*git,'verify-commit',commit)
if b'gpgsig -----BEGIN SSH SIGNATURE-----' not in command(*git,'cat-file','commit',commit):raise ValueError('signature missing')
keypath.unlink()
print('signing verification passed')
`

// Verify proves the resolved key matches the public profile and can sign and
// verify a disposable Git commit. It does not authenticate GitHub or a provider.
func (resolver Resolver) Verify(ctx context.Context, immutableRuntimeImage string) error {
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(immutableRuntimeImage) {
		return fmt.Errorf("signing verification requires an immutable runtime image")
	}
	key, err := resolver.Resolve(ctx)
	if err != nil {
		return err
	}
	defer clear(key)
	var identifier [12]byte
	if _, err := rand.Read(identifier[:]); err != nil {
		return fmt.Errorf("cannot identify signing verification container")
	}
	profile := sha256.Sum256([]byte(resolver.Profile.ID + "\n" + resolver.Profile.PublicKey))
	args := []string{"run", "--rm", "--name", "sdlc-signing-verify-" + hex.EncodeToString(identifier[:]), "--label", "io.sdlc.managed=true", "--label", "io.sdlc.kind=signing", "--label", "io.sdlc.profile=" + hex.EncodeToString(profile[:]), "--interactive", "--pull", "never", "--network", "none", "--user", "1000:1000", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "64", "--memory", "256m", "--cpus", "1", "--log-driver", "none", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=64m,mode=1777"}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "FTP_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "ftp_proxy", "no_proxy", "GH_TOKEN", "GITHUB_TOKEN", "SSH_AUTH_SOCK", "OP_SERVICE_ACCOUNT_TOKEN"} {
		args = append(args, "--env", name+"=")
	}
	args = append(args, "--entrypoint", "/usr/bin/timeout", immutableRuntimeImage, "--kill-after=5s", "45s", "/usr/bin/python3", "-c", verificationHelper, resolver.Profile.PublicKey, resolver.Profile.Fingerprint)
	ctx, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()
	run := resolver.Run
	if run == nil {
		run = localCommand
	}
	var output boundedOutput
	err = run(ctx, bytes.NewReader(key), &output, args...)
	clear(key)
	defer clear(output.Bytes())
	if err != nil || output.invalid || output.String() != verificationSuccess {
		return fmt.Errorf("signing verification failed; check the selected public identity and signing key")
	}
	return nil
}
