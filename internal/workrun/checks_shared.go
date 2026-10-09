package workrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/runtimepins"
	"github.com/tjpeel/sdlc/internal/testproxy"
)

const SharedTestDaemon = "shared-v1"
const testOwnerLabel = "io.sdlc.test-owner"
const testRecipeLabel = "io.sdlc.test-recipe"

type sharedChecks struct {
	checker                             DockerChecker
	name, directory, recipe, workVolume string
}

func newSharedChecks(checker DockerChecker, state runtimeimage.State, image string) (*sharedChecks, error) {
	if err := runtimepins.ValidateDaemonImage(image); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(checker.Runtime.Directory)
	if err != nil {
		return nil, err
	}
	key := sha256.Sum256([]byte(root + "\x00" + state.Engine))
	name := "sdlc-tests-" + hex.EncodeToString(key[:8])
	recipe := sha256.Sum256([]byte(SharedTestDaemon + "\x00" + image + "\x00overlay2"))
	directory := filepath.Join(root, "test-cache", name)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	return &sharedChecks{checker: checker, name: name, directory: directory, recipe: hex.EncodeToString(recipe[:]), workVolume: name + "-work"}, nil
}

func (s *sharedChecks) host(ctx context.Context, args ...string) ([]byte, error) {
	return s.checker.Runtime.Docker.Output(ctx, args...)
}
func (s *sharedChecks) prefix() []string {
	return []string{"exec", s.name, "docker", "--host", "unix://" + checkSocket}
}
func (s *sharedChecks) inner(ctx context.Context, args ...string) ([]byte, error) {
	return s.host(ctx, append(s.prefix(), args...)...)
}
func (s *sharedChecks) lease(ctx context.Context, name string) (*os.File, error) {
	return filelock.AcquireContext(ctx, filepath.Join(s.directory, name+".lock"), filelock.Exclusive, nil)
}

type daemonInspection struct {
	ID         string `json:"Id"`
	Image      string
	Config     struct{ Labels map[string]string }
	State      struct{ Running bool }
	HostConfig struct{ Privileged bool }
	Path       string
	Args       []string
	Mounts     []struct{ Type, Name, Destination string }
}

// The lock protects daemon setup and orphan recovery, never a running check.
func (s *sharedChecks) ensure(ctx context.Context, image string) error {
	lock, err := s.lease(ctx, "daemon")
	if err != nil {
		return err
	}
	defer lock.Close()
	ids, err := s.host(ctx, "ps", "-aq", "--filter", "name=^/"+s.name+"$")
	if err != nil {
		return err
	}
	if len(strings.Fields(string(ids))) > 1 {
		return errors.New("ambiguous persistent test daemon identity")
	}
	owners, err := s.host(ctx, "ps", "-aq", "--filter", "volume="+s.name+"-data")
	if err != nil {
		return err
	}
	for _, owner := range strings.Fields(string(owners)) {
		if owner != strings.TrimSpace(string(ids)) {
			return errors.New("another container references the persistent Docker data volume")
		}
	}
	for _, volume := range []string{s.name + "-data", s.workVolume, s.name + "-socket"} {
		found, err := s.host(ctx, "volume", "ls", "-q", "--filter", "name=^"+volume+"$")
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(found)) != "" {
			data, err := s.host(ctx, "volume", "inspect", "--format", "{{json .Labels}}", volume)
			var labels map[string]string
			if err != nil || json.Unmarshal(data, &labels) != nil || labels[testOwnerLabel] != s.name || labels[testRecipeLabel] != s.recipe {
				return errors.New("persistent test cache ownership or daemon pin differs; explicitly reset the idle cache before changing daemon versions")
			}
		} else {
			if _, err := s.host(ctx, "volume", "create", "--label", testOwnerLabel+"="+s.name, "--label", testRecipeLabel+"="+s.recipe, volume); err != nil {
				return err
			}
		}
	}
	if strings.TrimSpace(string(ids)) == "" {
		args := checkContainerEnvironment([]string{"run", "-d", "--name", s.name, "--pull", "never", "--privileged", "--label", testOwnerLabel + "=" + s.name, "--label", testRecipeLabel + "=" + s.recipe, "--mount", "type=volume,src=" + s.name + "-data,dst=/var/lib/docker", "--mount", "type=volume,src=" + s.workVolume + ",dst=/sdlc", "--mount", "type=volume,src=" + s.name + "-socket,dst=/run/sdlc", "--env", "DOCKER_TLS_CERTDIR="})
		args = append(args, "--entrypoint", "dockerd-entrypoint.sh", image, "--host=unix://"+checkSocket, "--tls=false", "--group=root", "--feature=containerd-snapshotter=false", "--storage-driver=overlay2")
		if _, err := s.host(ctx, args...); err != nil {
			return err
		}
	}
	data, err := s.host(ctx, "container", "inspect", s.name)
	var inspections []daemonInspection
	if err != nil || json.Unmarshal(data, &inspections) != nil || len(inspections) != 1 {
		return errors.New("cannot verify persistent test daemon")
	}
	c := inspections[0]
	expectedArgs := []string{"--host=unix://" + checkSocket, "--tls=false", "--group=root", "--feature=containerd-snapshotter=false", "--storage-driver=overlay2"}
	if c.Path != "dockerd-entrypoint.sh" || strings.Join(c.Args, "\x00") != strings.Join(expectedArgs, "\x00") {
		return errors.New("persistent test daemon command differs from the frozen recipe")
	}
	identity, err := s.host(ctx, "image", "inspect", "--format", "{{.Id}}", image)
	if err != nil || c.Image != strings.TrimSpace(string(identity)) || c.Config.Labels[testOwnerLabel] != s.name || c.Config.Labels[testRecipeLabel] != s.recipe || !c.HostConfig.Privileged {
		return errors.New("persistent test daemon identity or pin differs")
	}
	expected := map[string]string{"/var/lib/docker": s.name + "-data", "/sdlc": s.workVolume, "/run/sdlc": s.name + "-socket"}
	if len(c.Mounts) != len(expected) {
		return errors.New("persistent test daemon has unexpected mounts")
	}
	for _, mount := range c.Mounts {
		if mount.Type != "volume" || expected[mount.Destination] != mount.Name {
			return errors.New("persistent test daemon has unexpected mounts")
		}
	}
	if !c.State.Running {
		if _, err := s.host(ctx, "start", s.name); err != nil {
			return err
		}
	}
	ready, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	for {
		if _, err := s.inner(ready, "info", "--format", "{{.ID}}"); err == nil {
			break
		}
		select {
		case <-ready.Done():
			return errors.New("persistent test daemon did not become ready")
		case <-time.After(250 * time.Millisecond):
		}
	}
	return s.recover(ctx)
}

func (s *sharedChecks) recover(ctx context.Context) error {
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".session") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".session")
		if !validCheckSession(name) {
			return errors.New("invalid test session record")
		}
		lease, err := filelock.Acquire(filepath.Join(s.directory, name+".lock"))
		if errors.Is(err, filelock.ErrBusy) {
			continue
		}
		if err != nil {
			return err
		}
		err = s.cleanup(ctx, name)
		lease.Close()
		if err != nil {
			return fmt.Errorf("recover orphaned Docker checks: %w", err)
		}
	}
	return nil
}
func validCheckSession(name string) bool {
	if len(name) != 35 || !strings.HasPrefix(name, "sdlc-check-") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(name, "sdlc-check-"))
	return err == nil
}

// Seed is keyed by the immutable host image identity. No host Docker config,
// credentials or arbitrary image inventory is copied into the test daemon.
func (s *sharedChecks) seed(ctx context.Context, reference string, output io.Writer) error {
	if testproxy.ReservedImageReference(reference) {
		return errors.New("host image reference uses a reserved test-cache repository")
	}
	identity, err := s.host(ctx, "image", "inspect", "--format", "{{.Id}}", reference)
	if err != nil {
		return err
	}
	id := strings.TrimSpace(string(identity))
	if len(id) != 71 || !strings.HasPrefix(id, "sha256:") {
		return errors.New("invalid host image cache identity")
	}
	lock, err := s.lease(ctx, "image-"+strings.TrimPrefix(id, "sha256:"))
	if err != nil {
		return err
	}
	defer lock.Close()
	if _, err := s.inner(ctx, "image", "inspect", id); err != nil {
		fmt.Fprintf(output, "Reusing required host image %s\n", reference)
		reader, writer, err := os.Pipe()
		if err != nil {
			return err
		}
		save := exec.CommandContext(ctx, "docker", "image", "save", id)
		load := exec.CommandContext(ctx, "docker", "exec", "-i", s.name, "docker", "--host", "unix://"+checkSocket, "image", "load")
		save.Stdout, load.Stdin = writer, reader
		// Output from the image archive never reaches controller logs.
		if err = load.Start(); err != nil {
			reader.Close()
			writer.Close()
			return err
		}
		if err = save.Start(); err != nil {
			writer.Close()
			reader.Close()
			load.Process.Kill()
			load.Wait()
			return err
		}
		reader.Close()
		writer.Close()
		saveErr, loadErr := save.Wait(), load.Wait()
		if saveErr != nil || loadErr != nil {
			return errors.New("required host image transfer failed")
		}
		verified, err := s.inner(ctx, "image", "inspect", "--format", "{{.Id}}", id)
		if err != nil || strings.TrimSpace(string(verified)) != id {
			return errors.New("loaded image differs from immutable host identity")
		}
	}
	if _, err = s.inner(ctx, "image", "tag", id, "sdlc-test-base:"+strings.TrimPrefix(id, "sha256:")); err != nil {
		return err
	}
	if reference != id && !strings.Contains(reference, "@") {
		_, err = s.inner(ctx, "image", "tag", id, reference)
	}
	return err
}

func (s *sharedChecks) cleanup(ctx context.Context, name string) error {
	// A crashed controller can leave its source helper running on the host.
	helper, err := s.host(ctx, "ps", "-aq", "--filter", "label="+testOwnerLabel+"="+name)
	if err != nil {
		return err
	}
	for _, id := range strings.Fields(string(helper)) {
		if _, err := s.host(ctx, "rm", "-f", id); err != nil {
			return err
		}
	}
	for _, kind := range []string{"container", "network", "volume"} {
		for _, label := range []string{testOwnerLabel, testproxy.SessionLabel} {
			list := []string{kind, "ls", "-q", "--filter", "label=" + label + "=" + name}
			if kind == "container" {
				list = append(list, "-a")
			}
			data, err := s.inner(ctx, list...)
			if err != nil {
				return err
			}
			for _, id := range strings.Fields(string(data)) {
				remove := []string{kind, "rm"}
				if kind == "container" {
					remove = append(remove, "-f")
				}
				remove = append(remove, id)
				if _, err := s.inner(ctx, remove...); err != nil {
					return err
				}
			}
		}
	}
	// Keep immutable cache tags even when Compose did not remove local build tags.
	data, err := s.inner(ctx, "image", "ls", "-q", "--filter", "label="+testproxy.SessionLabel+"="+name)
	if err != nil {
		return err
	}
	for _, id := range strings.Fields(string(data)) {
		full, err := s.inner(ctx, "image", "inspect", "--format", "{{.Id}}", id)
		if err != nil {
			return err
		}
		identity := strings.TrimSpace(string(full))
		if len(identity) != 71 || !strings.HasPrefix(identity, "sha256:") {
			return errors.New("invalid retained cache image identity")
		}
		if _, err := s.inner(ctx, "image", "tag", identity, "sdlc-test-cache:"+strings.TrimPrefix(identity, "sha256:")); err != nil {
			return err
		}
		tags, err := s.inner(ctx, "image", "inspect", "--format", "{{json .RepoTags}}", id)
		if err != nil {
			return err
		}
		var references []string
		if json.Unmarshal(tags, &references) != nil {
			return errors.New("invalid session image tags")
		}
		for _, tag := range references {
			if strings.HasPrefix(tag, "sdlc-test/"+name+":") {
				if _, err := s.inner(ctx, "image", "rm", tag); err != nil {
					return err
				}
			}
		}
	}
	if _, err := s.host(ctx, "exec", s.name, "rm", "-rf", "/sdlc/workspaces/"+name, "/sdlc/sockets/"+name); err != nil {
		return err
	}
	return os.Remove(filepath.Join(s.directory, name+".session"))
}

func (checker DockerChecker) checkShared(ctx context.Context, workspace, inputs string, commands [][]string, output io.Writer, baseline *VerificationBaseline, name string, state runtimeimage.State, daemonImage string) (result error) {
	if output == nil {
		output = io.Discard
	}
	s, err := newSharedChecks(checker, state, daemonImage)
	if err != nil {
		return err
	}
	if err = s.ensure(ctx, daemonImage); err != nil {
		return err
	}
	lease, err := s.lease(ctx, name)
	if err != nil {
		return err
	}
	defer lease.Close()
	if err = os.WriteFile(filepath.Join(s.directory, name+".session"), []byte(name+"\n"), 0600); err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if err := s.cleanup(cleanup, name); err != nil {
			if errors.Is(result, ErrCheckFailed) {
				result = fmt.Errorf("%s", result)
			}
			result = errors.Join(result, fmt.Errorf("shared check cleanup failed: %w", err))
		}
	}()
	if err = s.seed(ctx, state.ImageID, output); err != nil {
		return err
	}
	root, socket := "/sdlc/workspaces/"+name, "/sdlc/sockets/"+name
	if _, err = s.host(ctx, "exec", s.name, "mkdir", "-p", root, socket); err != nil {
		return err
	}
	if _, err = s.host(ctx, "exec", s.name, "chown", "0:1000", socket); err != nil {
		return err
	}
	if _, err = s.host(ctx, "exec", s.name, "chmod", "0750", socket); err != nil {
		return err
	}
	tree, err := isolatedGit(ctx, workspace, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return err
	}
	key := strings.TrimSpace(string(tree))
	if len(key) != 40 && len(key) != 64 {
		return errors.New("invalid check source tree")
	}
	sourceLease, err := s.lease(ctx, "source-"+key)
	if err != nil {
		return err
	}
	copyArgs := checkContainerEnvironment([]string{"run", "--name", name + "-copy", "--label", testOwnerLabel + "=" + name, "--pull", "never", "--network", "none", "--user", "0:0", "--read-only", "--cap-drop", "ALL", "--cap-add", "CHOWN", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges", "--mount", "type=bind,src=" + workspace + ",dst=/source,readonly", "--mount", "type=volume,src=" + s.workVolume + ",dst=/sdlc", "--env", "SDLC_CHECK_WORKSPACE=" + root, "--env", "SDLC_CHECK_TEMPLATES=/sdlc/templates"})
	copyArgs = append(copyArgs, "--env", "SDLC_CHECK_TREE="+key)
	if inputs != "" {
		copyArgs = append(copyArgs, "--mount", "type=bind,src="+inputs+",dst=/inputs,readonly")
	}
	copyArgs = append(copyArgs, "--entrypoint", "python3", state.ImageID, "-c", copyCheckWorkspace)
	if baseline != nil {
		data, _ := json.Marshal(baseline)
		copyArgs = append(copyArgs, string(data))
	}
	started := time.Now()
	message, err := s.host(ctx, copyArgs...)
	sourceLease.Close()
	if err != nil {
		return fmt.Errorf("copy cached source snapshot: %w", err)
	}
	fmt.Fprintf(output, "%sSource preparation: %s\n", message, time.Since(started).Round(time.Millisecond))
	// Resolve Compose image references inside the credential-free runtime so
	// frozen .env inputs and installed Compose parsing match the real checks.
	seedArgs := checkContainerEnvironment([]string{"run", "--name", name + "-seed", "--label", testOwnerLabel + "=" + name, "--pull", "never", "--network", "none", "--user", "1000:1000", "--read-only", "--cap-drop", "ALL", "--workdir", "/workspace", "--mount", "type=volume,src=" + s.workVolume + ",dst=/workspace,volume-subpath=workspaces/" + name, "--tmpfs", "/tmp:rw,nosuid,nodev,mode=1777", "--env", "HOME=/tmp", "--env", "DOCKER_CONFIG=/tmp/empty-docker", "--entrypoint", "python3"})
	seedArgs = append(seedArgs, state.ImageID, "-c", checkSeedReferences)
	if images, err := s.host(ctx, seedArgs...); err == nil {
		for _, reference := range strings.Fields(string(images)) {
			if _, err := s.inner(ctx, "image", "inspect", reference); err == nil {
				continue
			}
			if _, err := s.host(ctx, "image", "inspect", reference); err != nil {
				continue
			}
			if err := s.seed(ctx, reference, output); err != nil {
				return err
			}
		}
	}
	// Infrastructure is attributed separately from objects visible to checks.
	if _, err = s.inner(ctx, "network", "create", "--label", testOwnerLabel+"="+name, name+"-default"); err != nil {
		return err
	}
	proxyArgs := checkContainerEnvironment([]string{"run", "-d", "--name", name + "-proxy", "--label", testOwnerLabel + "=" + name, "--pull", "never", "--user", "0:1000", "--network", name + "-default", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--mount", "type=bind,src=" + checkSocket + ",dst=/sdlc/daemon/docker.sock", "--mount", "type=bind,src=" + root + ",dst=/workspace,readonly", "--mount", "type=bind,src=" + socket + ",dst=/run/sdlc", "--entrypoint", "/usr/local/bin/sdlc-test-proxy"})
	proxyArgs = append(proxyArgs, state.ImageID, "--session", name, "--workspace", root, "--socket-source", socket+"/docker.sock")
	if _, err = s.inner(ctx, proxyArgs...); err != nil {
		return err
	}
	ready, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for {
		if _, err := s.host(ready, "exec", s.name, "test", "-S", socket+"/docker.sock"); err == nil {
			break
		}
		select {
		case <-ready.Done():
			logs, _ := s.inner(ctx, "logs", "--tail", "20", name+"-proxy")
			return fmt.Errorf("scoped Docker proxy did not start; rebuild the runtime to include sdlc-test-proxy: %s", logs)
		case <-time.After(100 * time.Millisecond):
		}
	}
	fmt.Fprintln(output, "Shared Docker image/build cache ready; check session isolated")
	return checker.executeChecks(ctx, workspace, commands, output, baseline, name, state.ImageID, "container:"+name+"-proxy", "type=bind,src="+root+",dst=/workspace", "type=bind,src="+socket+",dst=/run/sdlc", s.prefix(), func(string) {})
}

// Resolve only the selected project's declarative image references. The helper
// has no network or host credentials. Complex unresolved references simply use
// the normal check pull path, preserving the project's pull policy.
const checkSeedReferences = `import json,pathlib,re,subprocess
root=pathlib.Path('/workspace').resolve()
project=json.loads(subprocess.check_output(['docker','compose','--profile','*','config','--format','json'],stderr=subprocess.DEVNULL))
references=set()
for service in project.get('services',{}).values():
 if service.get('image'): references.add(service['image'])
 build=service.get('build')
 if not isinstance(build,dict): continue
 context=pathlib.Path(build.get('context','/workspace')).resolve()
 file=(context/build.get('dockerfile','Dockerfile')).resolve()
 if not file.is_relative_to(root) or not file.is_file() or file.stat().st_size>1048576: continue
 source=build.get('dockerfile_inline') or file.read_text()
 args={};stages=set();global_args=True
 for line in source.splitlines():
  fields=line.strip().split()
  if len(fields)<2: continue
  if fields[0].upper()=='ARG' and global_args:
   name,sep,value=fields[1].partition('=')
   supplied=build.get('args',{}).get(name)
   if supplied is not None: args[name]=str(supplied)
   elif sep: args[name]=value
  if fields[0].upper()=='FROM':
   global_args=False
   image=fields[2] if fields[1].startswith('--platform=') and len(fields)>2 else fields[1]
   image=re.sub(r'\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)',lambda m:args.get(m[1] or m[2],m[0]),image)
   if image.lower() not in stages and image!='scratch': references.add(image)
   if len(fields)>=4 and fields[-2].upper()=='AS': stages.add(fields[-1].lower())
  elif fields[0].upper()=='COPY':
   for field in fields[1:]:
    if field.startswith('--from=') and field[7:].lower() not in stages: references.add(field[7:])
for reference in sorted(references):
 if re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._:@/-]*',reference): print(reference)
`
