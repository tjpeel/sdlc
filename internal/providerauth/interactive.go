package providerauth

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/instructions"
)

func bindInstructions(source, destination string) string {
	var output strings.Builder
	writer := csv.NewWriter(&output)
	_ = writer.Write([]string{"type=bind", "src=" + source, "dst=" + destination, "readonly"})
	writer.Flush()
	return strings.TrimSuffix(output.String(), "\n")
}

// InteractiveMode validates a native provider mode and supplies its full-access default.
func InteractiveMode(provider, mode string) (string, error) {
	if provider == "codex" {
		if mode == "" {
			mode = "never"
		}
		if mode != "never" && mode != "on-request" {
			return "", fmt.Errorf("Codex approval must be never or on-request")
		}
		return mode, nil
	}
	if provider == "claude" {
		if mode == "" {
			mode = "bypassPermissions"
		}
		switch mode {
		case "default", "manual", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions":
			return mode, nil
		default:
			return "", fmt.Errorf("Claude permission mode must be default, manual, acceptEdits, plan, auto, dontAsk or bypassPermissions")
		}
	}
	return "", fmt.Errorf("provider must be codex or claude")
}

func interactiveArgs(image, name, volume, provider, snapshot, settings, mode string) []string {
	args := containerArgs(image, name, volume, provider, "interactive")
	args = append(args, mode)
	extra := []string{"--init", "--workdir", "/workspace",
		"--tmpfs", "/workspace:rw,nosuid,nodev,size=1g,mode=0700,uid=1000,gid=1000",
		"--mount", bindInstructions(snapshot, "/session-instructions.md")}
	if provider == "claude" {
		extra = append(extra, "--mount", bindInstructions(snapshot, "/provider-auth/CLAUDE.md"),
			"--mount", bindInstructions(settings, "/session-settings.json"),
			"--mount", bindInstructions(settings, "/provider-auth/settings.json"),
			"--tmpfs", "/provider-auth/projects:rw,nosuid,nodev,noexec,size=256m,mode=0700,uid=1000,gid=1000",
			"--tmpfs", "/provider-auth/plugins:rw,nosuid,nodev,noexec,size=64m,mode=0700,uid=1000,gid=1000")
		// Native Claude cache layout follows the pinned client. Keep work data
		// and extra personal customizations out of the persistent account cache.
		for _, directory := range []string{"rules", "commands", "output-styles", "workflows", "agent-memory",
			"file-history", "plans", "debug", "paste-cache", "image-cache", "uploads", "dev-mods",
			"session-env", "tasks", "shell-snapshots", "feedback-bundles", "feedback", "usage-data",
			"sessions", "todos", "statsig", "logs"} {
			extra = append(extra, "--tmpfs", "/provider-auth/"+directory+":rw,nosuid,nodev,noexec,size=64m,mode=0700,uid=1000,gid=1000")
		}
	}
	for i, arg := range args {
		if arg == "--entrypoint" {
			result := append([]string(nil), args[:i]...)
			result = append(result, extra...)
			return append(result, args[i:]...)
		}
	}
	panic("authentication container entrypoint is missing")
}

// Interactive attaches the user's terminal to the selected official client.
// It shares only that provider's native login cache and private instructions.
func (manager Manager) Interactive(ctx context.Context, provider, mode string) (err error) {
	mode, err = InteractiveMode(provider, mode)
	if err != nil {
		return err
	}
	if !manager.Terminal() {
		return fmt.Errorf("interactive sessions need a terminal for stdin, stdout and stderr; do not redirect output")
	}
	state, lock, err := manager.begin(ctx)
	if err != nil {
		return err
	}
	defer lock.Close()
	id, err := manager.identity(false)
	if err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("no stored account login; run sdlc auth login --provider %s", provider)
	}
	volume, err := manager.volume(ctx, id, provider, false)
	if err != nil {
		return err
	}
	if volume == "" {
		return fmt.Errorf("no stored account login; run sdlc auth login --provider %s", provider)
	}
	preflight, cancel := context.WithTimeout(ctx, time.Minute)
	status, err := manager.status(preflight, state.ImageID, volume, provider)
	cancel()
	if err != nil {
		return err
	}
	if status != "stored" {
		return fmt.Errorf("stored account login is unavailable; run sdlc auth login --provider %s", provider)
	}
	content, err := (instructions.Manager{Directory: manager.Runtime.Directory}).Show()
	if err != nil {
		return fmt.Errorf("cannot load shared instructions; check sdlc instructions show")
	}
	directory, err := os.MkdirTemp(manager.Runtime.Directory, ".interactive-")
	if err != nil {
		return fmt.Errorf("cannot prepare private session instructions")
	}
	defer os.RemoveAll(directory)
	snapshot := filepath.Join(directory, "instructions.md")
	// The parent is 0700 on the host. Docker's non-root container UID needs
	// read access to this individual, read-only bind mount on every host OS.
	if err := os.WriteFile(snapshot, content, 0444); err != nil {
		return fmt.Errorf("cannot snapshot shared instructions")
	}
	if err := os.Chmod(snapshot, 0444); err != nil {
		return fmt.Errorf("cannot make session instructions readable inside the container")
	}
	absolute, err := filepath.Abs(snapshot)
	if err != nil {
		return fmt.Errorf("cannot locate session instructions")
	}
	settings := ""
	if provider == "claude" {
		settings = filepath.Join(filepath.Dir(absolute), "settings.json")
		content := "{}\n"
		if mode == "bypassPermissions" {
			// The user selected full access through SDLC. Use the documented
			// setting to avoid repeating acknowledgement in disposable sessions.
			content = "{\"skipDangerousModePermissionPrompt\":true}\n"
		}
		if err := os.WriteFile(settings, []byte(content), 0444); err != nil {
			return fmt.Errorf("cannot prepare isolated provider settings")
		}
		if err := os.Chmod(settings, 0444); err != nil {
			return fmt.Errorf("cannot make session settings readable inside the container")
		}
	}
	nameID, err := randomID()
	if err != nil {
		return fmt.Errorf("cannot name interactive container")
	}
	name := "sdlc-interactive-" + nameID
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		remaining, cleanupErr := manager.Docker.Output(cleanup, "ps", "--all", "--filter", "name=^/"+name+"$", "--format", "{{.ID}}")
		if cleanupErr == nil && len(strings.TrimSpace(string(remaining))) > 0 {
			_, cleanupErr = manager.Docker.Output(cleanup, "rm", "--force", name)
		}
		if cleanupErr != nil {
			err = fmt.Errorf("interactive container cleanup failed; inspect Docker before starting another session")
		}
	}()
	if err := manager.Docker.Interactive(ctx, interactiveArgs(state.ImageID, name, volume, provider, absolute, settings, mode)...); err != nil {
		return fmt.Errorf("interactive %s session failed or was cancelled; run sdlc auth status --provider %s before retrying", provider, provider)
	}
	return nil
}
