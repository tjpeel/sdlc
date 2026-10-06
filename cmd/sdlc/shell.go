package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tjpeel/sdlc/internal/buildinfo"
	"github.com/tjpeel/sdlc/internal/dashboard"
	"github.com/tjpeel/sdlc/internal/project"
	"github.com/tjpeel/sdlc/internal/shell"
	"github.com/tjpeel/sdlc/internal/terminallaunch"
)

type shellUsageError struct{ error }

func promptIdentity(ctx context.Context, root string) (string, bool) {
	identity, err := project.InspectIdentity(ctx, root)
	return dashboard.SafeText(identity.Branch), err == nil && identity.Dirty
}

func shellCommand(ctx context.Context, args []string, input, output *os.File) error {
	flags := flag.NewFlagSet("shell", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	plain := flags.Bool("plain", false, "interactive line mode")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(output, "Usage: sdlc shell [--plain]\nOpen an opt-in interactive frontend over the existing CLI. Jobs run in independent terminals.")
			return nil
		}
		return shellUsageError{err}
	}
	if flags.NArg() != 0 {
		return shellUsageError{fmt.Errorf("shell accepts only --plain")}
	}
	if !shell.IsTerminal(input) || !shell.IsTerminal(output) {
		return shellUsageError{fmt.Errorf("sdlc shell requires terminal input and output; use the existing CLI commands with redirected I/O")}
	}
	if os.Getenv("TERM") == "dumb" && !*plain {
		return shellUsageError{fmt.Errorf("this terminal requires sdlc shell --plain")}
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	if projectRoot, err := checkoutRoot(ctx, root); err == nil {
		root = projectRoot
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	branch, dirty := promptIdentity(ctx, root)
	adapter := &shellAdapter{executable: executable, plans: map[string]string{}}
	config := shell.Config{Root: root, Version: buildinfo.Version, Branch: branch, Dirty: dirty, Commands: shellCommands(), Input: input, Output: output,
		Read: adapter.read, Execute: adapter.execute, Launch: adapter.launch, Complete: shellCompletion, Prompt: promptIdentity,
		ResolveRun: adapter.resolveRun, RespondRun: adapter.respondRun, ResumeRun: adapter.resumeRun,
		SelectProject: func(ctx context.Context, current, name string) (string, error) {
			projects, err := projectList(ctx)
			if err != nil {
				return "", err
			}
			for _, p := range projects {
				if p.Name == name {
					return checkoutRoot(ctx, p.Root)
				}
			}
			return "", fmt.Errorf("unknown project; register it with /project add PATH --name NAME")
		},
	}
	if *plain {
		return shell.RunPlain(ctx, config)
	}
	return shell.Run(ctx, config)
}

type shellAdapter struct {
	executable string
	mu         sync.Mutex
	plans      map[string]string
	launchID   string
	launchRoot string
}

func normaliseShellArgs(args []string) ([]string, error) {
	args = append([]string(nil), args...)
	if len(args) == 0 {
		return nil, fmt.Errorf("choose a command")
	}
	if args[0] != "run" {
		return args, nil
	}
	reference, ticket := "", ""
	for i := 1; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "-") {
			continue
		}
		name, value, assigned := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		if name != "reference" && name != "ticket" {
			if (runValueFlags[name] || name == "terminal" || name == "launch-id") && !assigned {
				i++
			}
			continue
		}
		if !assigned {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--%s requires a value", name)
			}
			i++
			value = args[i]
		}
		if name == "reference" {
			reference = value
		}
		if name == "ticket" && strings.HasPrefix(value, "@") {
			ref, file, ok := strings.Cut(strings.TrimPrefix(value, "@"), "/")
			if !ok || ref == "" || file == "" || filepath.Base(file) != file {
				return nil, fmt.Errorf("ticket locator must be @REFERENCE/NUMBERED_FILE")
			}
			ticket = ref
			if assigned {
				args[i] = "--ticket=" + file
			} else {
				args[i] = file
			}
		}
	}
	if ticket != "" {
		if reference != "" && ticket != reference {
			return nil, fmt.Errorf("ticket locator conflicts with --reference")
		}
		if reference == "" {
			args = append(args, "--reference", ticket)
		}
	}
	return args, nil
}

func (a *shellAdapter) execute(ctx context.Context, root string, args []string) (*exec.Cmd, error) {
	args, err := normaliseShellArgs(args)
	if err != nil {
		return nil, err
	}
	if args[0] == "run" {
		return nil, fmt.Errorf("use the launch review to start a run")
	}
	matched := ""
	native := false
	for _, command := range shellCommands() {
		words := strings.Fields(command.Name)
		if len(words) <= len(args) && strings.Join(args[:len(words)], " ") == command.Name && len(command.Name) > len(matched) {
			matched, native = command.Name, command.Native
		}
	}
	if matched == "" || !native {
		return nil, fmt.Errorf("choose a supported native command with /help")
	}
	command := exec.CommandContext(ctx, a.executable, args...)
	command.Dir = root
	return command, nil
}

func (a *shellAdapter) read(ctx context.Context, root string, args []string) (string, error) {
	args, err := normaliseShellArgs(args)
	if err != nil {
		return "", err
	}
	if args[0] == "run" {
		plan, err := offlinePlan(ctx, args[1:], root)
		if err != nil {
			return "", err
		}
		a.mu.Lock()
		a.plans[planKey(root, args)] = planHash(plan)
		a.mu.Unlock()
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, plan, "", "  "); err != nil {
			return "", err
		}
		return pretty.String(), nil
	}
	if args[0] == "version" && len(args) == 1 {
		args = append(args, "--details")
	}
	if args[0] == "version" {
		// Preserve the identity of this already-running shell after an install
		// replaces the executable on PATH. Other commands use the CLI normally.
		var output boundedBuffer
		err := versionDetailsCommand(ctx, args[1:], &output)
		return dashboard.SafeText(output.String()), err
	}
	if args[0] == "onboard" && len(args) == 1 {
		args = append(args, "status")
	}
	if args[0] == "dashboard" {
		normalised := []string{}
		hasScope := false
		for i, arg := range args {
			if arg == "--watch" || strings.HasPrefix(arg, "--watch=") {
				continue
			}
			if arg == "--scope" || strings.HasPrefix(arg, "--scope=") {
				hasScope = true
			}
			if arg == "--scope=all" {
				arg = "--scope=installation"
			}
			if arg == "all" && i > 0 && args[i-1] == "--scope" {
				arg = "installation"
			}
			normalised = append(normalised, arg)
		}
		args = normalised
		if !hasScope {
			args = append(args, "--scope", "project")
		}
		if !containsArg(args, "--once") && !containsArg(args, "--json") {
			args = append(args, "--once")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, a.executable, args...)
	command.Dir = root
	var output, diagnostics boundedBuffer
	command.Stdout, command.Stderr = &output, &diagnostics
	err = command.Run()
	text := dashboard.SafeText(output.String())
	if err != nil {
		message := strings.TrimSpace(dashboard.SafeText(diagnostics.String()))
		if message == "" {
			message = err.Error()
		}
		return text, errors.New(message)
	}
	if args[0] == "dashboard" {
		a.mu.Lock()
		id, launchRoot := a.launchID, a.launchRoot
		a.mu.Unlock()
		if id != "" && launchRoot == root {
			store, storeErr := launchStore("")
			if storeErr == nil {
				if receipt, receiptErr := store.Status(id); receiptErr == nil {
					text = fmt.Sprintf("Launch %s: %s\n%s\n\n", receipt.ID, receipt.State, dashboard.SafeText(receipt.Error)) + text
					for _, runID := range receipt.RunIDs {
						var detail bytes.Buffer
						if err := dashboardCommand(ctx, []string{"--once", "--run", runID, "--logs"}, &detail); err == nil {
							text += "\n" + dashboard.SafeText(detail.String())
						}
					}
				}
			}
		}
	}
	return text, nil
}

func planKey(root string, args []string) string {
	clean := withoutRunFlags(args, "dry-run", "json", "terminal", "launch-id")
	data, _ := json.Marshal(struct {
		Root string
		Args []string
	}{root, clean})
	return string(data)
}

func (a *shellAdapter) launch(ctx context.Context, root string, args []string) (string, error) {
	args, err := normaliseShellArgs(args)
	if err != nil {
		return "", err
	}
	if args[0] != "run" {
		return "", fmt.Errorf("only the existing run controller can be launched")
	}
	options, err := parseRunOptions(args[1:])
	if err != nil {
		return "", err
	}
	if options.dryRun {
		return "", fmt.Errorf("--dry-run is preview only; remove it and review again before Start")
	}
	plan, err := offlinePlan(ctx, args[1:], root)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	previous := a.plans[planKey(root, args)]
	a.mu.Unlock()
	if previous == "" || previous != planHash(plan) {
		return "", fmt.Errorf("launch plan changed or was not reviewed; run the command again to review it")
	}
	id, err := terminallaunch.NewID()
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	a.launchID, a.launchRoot = id, root
	a.mu.Unlock()
	var output bytes.Buffer
	err = launchPreparedRun(ctx, root, controllerArgs(args[1:]), previous, id, true, &output, terminallaunch.ITerm2{})
	return dashboard.SafeText(output.String()), err
}

func containsArg(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}

// Command output is bounded before it reaches the terminal model.
type boundedBuffer struct{ bytes.Buffer }

func (b *boundedBuffer) Write(data []byte) (int, error) {
	n := len(data)
	remaining := 256*1024 - b.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = b.Buffer.Write(data)
	}
	return n, nil
}

func shellCompletion(ctx context.Context, root, draft string) ([]shell.Suggestion, error) {
	position := strings.LastIndex(draft, "@")
	refCommand := strings.HasPrefix(draft, "/reference ")
	if position < 0 && !refCommand {
		return nil, nil
	}
	result, err := project.References(ctx, root)
	if err != nil {
		return nil, err
	}
	prefix, needle := draft[:position+1], draft[position+1:]
	if refCommand {
		prefix, needle = "/reference ", strings.TrimPrefix(draft, "/reference ")
	}
	needle = strings.Trim(needle, "\"'")
	var suggestions []shell.Suggestion
	for _, ref := range result.References {
		if ref.Error != "" {
			continue
		}
		if refCommand {
			if strings.HasPrefix(ref.Name, needle) {
				suggestions = append(suggestions, shell.Suggestion{Label: ref.Name, Insert: prefix + strconv.Quote(ref.Name), Description: fmt.Sprintf("%d local tickets", len(ref.Tickets))})
			}
			continue
		}
		for _, ticket := range ref.Tickets {
			value := ref.Name + "/" + filepath.Base(ticket)
			if strings.HasPrefix(value, needle) {
				suggestions = append(suggestions, shell.Suggestion{Label: "@" + value, Insert: draft[:position] + strconv.Quote("@"+value), Description: "Local ticket metadata; inspect explicitly to read content"})
			}
			if len(suggestions) >= 100 {
				return suggestions, nil
			}
		}
	}
	return suggestions, nil
}
