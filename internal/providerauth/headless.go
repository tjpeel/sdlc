package providerauth

import (
	"context"
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

//go:embed headless.py
var headlessHelper string

// HeadlessRequest selects one official client and its private native session.
// Directory and file paths must be absolute. SessionDirectory is writable by
// container UID 1000; Workspace needs that access only for implementation work.
type HeadlessRequest struct {
	Provider, Model, Effort, Workspace, SessionDirectory, PromptFile, SchemaFile, ResumeID string
	ImageID, InstructionsFile                                                              string
	ReadOnly                                                                               bool
}

// DockerStreamer extends Docker without requiring terminal-only adapters to stream.
type DockerStreamer interface {
	Stream(context.Context, io.Reader, io.Writer, io.Writer, ...string) error
}

func (LocalDocker) Stream(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
	command := exec.CommandContext(ctx, "docker", args...)
	command.Stdin, command.Stdout, command.Stderr = stdin, stdout, stderr
	for i, arg := range args {
		if arg == "--name" && i+1 < len(args) {
			name := args[i+1]
			command.Cancel = func() error {
				stop, cancel := context.WithTimeout(context.Background(), 12*time.Second)
				defer cancel()
				return exec.CommandContext(stop, "docker", "stop", "--time", "8", name).Run()
			}
			command.WaitDelay = 15 * time.Second
			break
		}
	}
	return command.Run()
}

var headlessValue = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)
var sessionID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func validateHeadless(request HeadlessRequest) error {
	if !validProvider(request.Provider) || !headlessValue.MatchString(request.Model) || !headlessValue.MatchString(request.Effort) {
		return fmt.Errorf("headless work requires a provider, model and effort")
	}
	if request.ResumeID != "" && !sessionID.MatchString(request.ResumeID) {
		return fmt.Errorf("headless resume requires a specific native session ID")
	}
	if request.InstructionsFile != "" {
		info, err := os.Lstat(request.InstructionsFile)
		if err != nil || !filepath.IsAbs(request.InstructionsFile) || !info.Mode().IsRegular() {
			return fmt.Errorf("shared instructions must be an absolute regular file")
		}
	}
	for _, item := range []struct {
		path      string
		directory bool
	}{{request.Workspace, true}, {request.SessionDirectory, true}, {request.PromptFile, false}, {request.SchemaFile, false}} {
		if !filepath.IsAbs(item.path) || filepath.Clean(item.path) != item.path {
			return fmt.Errorf("headless paths must be absolute and clean")
		}
		info, err := os.Lstat(item.path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || (item.directory && !info.IsDir()) || (!item.directory && !info.Mode().IsRegular()) {
			return fmt.Errorf("headless input paths must be existing directories or regular files")
		}
	}
	for _, directory := range []string{".codex", ".claude", ".agents"} {
		if info, err := os.Lstat(filepath.Join(request.Workspace, directory)); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return fmt.Errorf("repository provider configuration must use regular directories")
		}
	}
	relative, err := filepath.Rel(request.Workspace, request.SessionDirectory)
	if err != nil || relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return fmt.Errorf("private native session state must be outside the workspace")
	}
	return nil
}

func headlessBind(source, destination string, readonly bool) string {
	var output strings.Builder
	writer := csv.NewWriter(&output)
	fields := []string{"type=bind", "src=" + source, "dst=" + destination}
	if readonly {
		fields = append(fields, "readonly")
	}
	_ = writer.Write(fields)
	writer.Flush()
	return strings.TrimSuffix(output.String(), "\n")
}

func headlessArgs(image, name, volume string, request HeadlessRequest) []string {
	args := containerArgs(image, name, volume, request.Provider, "headless")
	entry := 0
	for i, arg := range args {
		if arg == "--entrypoint" {
			entry = i
			break
		}
	}
	args = args[:entry]
	for i, arg := range args {
		switch arg {
		case "--memory":
			args[i+1] = "4g"
		case "--cpus":
			args[i+1] = "4"
		case "--pids-limit":
			args[i+1] = "512"
		}
	}
	args = append(args, "--init", "--interactive", "--network", "bridge", "--workdir", "/workspace",
		"--mount", headlessBind(request.Workspace, "/workspace", request.ReadOnly),
		"--mount", headlessBind(request.SessionDirectory, "/session", false),
		"--mount", headlessBind(request.PromptFile, "/prompt.txt", true),
		"--mount", headlessBind(request.SchemaFile, "/schema.json", true))
	if request.InstructionsFile != "" {
		args = append(args, "--mount", headlessBind(request.InstructionsFile, "/instructions.md", true))
	}
	// Hide repository extensions/configuration while retaining pinned image skills
	// and agents. Mount existing paths only: Docker cannot create new mountpoints
	// in a read-only reviewer checkout.
	for _, directory := range []string{".codex", ".claude", ".agents"} {
		if info, err := os.Lstat(filepath.Join(request.Workspace, directory)); err == nil && info.IsDir() {
			args = append(args, "--tmpfs", "/workspace/"+directory+":rw,nosuid,nodev,noexec,size=1m,mode=0700,uid=1000,gid=1000")
		}
	}
	encoded, _ := json.Marshal(helper)
	script := "import types\n_auth={'__name__':'sdlc_auth'}\nexec(" + string(encoded) + ", _auth)\nnative=types.SimpleNamespace(**_auth)\n" + headlessHelper
	readonly := "false"
	if request.ReadOnly {
		readonly = "true"
	}
	return append(args, "--entrypoint", "/usr/bin/python3", image, "-c", script, request.Provider, request.Model, request.Effort, request.ResumeID, readonly)
}

// Headless streams native JSONL and private diagnostics while holding the same
// runtime lease used for authentication preflight. It never changes identities.
func (manager Manager) Headless(ctx context.Context, request HeadlessRequest, stdout, stderr io.Writer) (err error) {
	if err = validateHeadless(request); err != nil {
		return err
	}
	streamer, ok := manager.Docker.(DockerStreamer)
	if !ok {
		return fmt.Errorf("Docker adapter does not support native headless sessions")
	}
	state, lock, err := manager.begin(ctx, request.Provider)
	if err != nil {
		return err
	}
	defer lock.Close()
	if request.ImageID != "" && state.ImageID != request.ImageID {
		return fmt.Errorf("runtime changed since launch; restore the recorded image before resuming")
	}
	id, err := manager.identityContext(ctx, false)
	if err != nil {
		return err
	}
	missing := fmt.Errorf("stored account login is unavailable; run sdlc auth login --provider %s", request.Provider)
	if id == "" {
		return missing
	}
	volume, err := manager.volume(ctx, id, request.Provider, false)
	if err != nil {
		return err
	}
	if volume == "" {
		return missing
	}
	preflight, cancel := context.WithTimeout(ctx, time.Minute)
	status, err := manager.status(preflight, state.ImageID, volume, request.Provider)
	cancel()
	if err != nil {
		return err
	}
	if status != "stored" {
		return missing
	}
	token, err := randomID()
	if err != nil {
		return fmt.Errorf("cannot name headless container")
	}
	name := "sdlc-headless-" + token
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		remaining, cleanupErr := manager.Docker.Output(cleanup, "ps", "--all", "--filter", "name=^/"+name+"$", "--format", "{{.ID}}")
		if cleanupErr == nil && strings.TrimSpace(string(remaining)) != "" {
			_, cleanupErr = manager.Docker.Output(cleanup, "rm", "--force", name)
		}
		if cleanupErr != nil {
			err = fmt.Errorf("headless container cleanup failed; inspect Docker before another run")
		}
	}()
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if streamErr := streamer.Stream(ctx, nil, stdout, stderr, headlessArgs(state.ImageID, name, volume, request)...); streamErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("native %s headless session failed; inspect the private run log before retrying", request.Provider)
	}
	return nil
}
