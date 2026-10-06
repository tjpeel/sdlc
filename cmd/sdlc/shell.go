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
	"github.com/tjpeel/sdlc/internal/runprogress"
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
		Read: adapter.read, Execute: adapter.execute, Launch: adapter.launch, Progress: adapter.progress, Complete: shellCompletion, Prompt: promptIdentity,
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

// The CLI owns log access, cursors, producer attribution and run liveness.
func (a *shellAdapter) progress(ctx context.Context, root string, selectors []string, cursor string) (runprogress.Batch, error) {
	args := []string{"progress", "--once", "--json"}
	hasSelection := false
	if len(selectors) > 0 {
		validated := append([]string(nil), selectors[1:]...)
		for i := range validated {
			if validated[i] == "--scope=all" {
				validated[i] = "--scope=installation"
			}
			if validated[i] == "all" && i > 0 && validated[i-1] == "--scope" {
				validated[i] = "installation"
			}
		}
		if selectors[0] == "dashboard" {
			if _, err := parseDashboardOptions(validated); err != nil {
				return runprogress.Batch{}, err
			}
		} else {
			o, err := parseProgressOptions(validated)
			if err != nil {
				return runprogress.Batch{}, err
			}
			if cursor == "" {
				cursor = o.cursor
			}
		}
	}
	for i := 1; i < len(selectors); i++ {
		name, value, assigned := strings.Cut(strings.TrimPrefix(selectors[i], "--"), "=")
		if name != "run" && name != "launch-id" && name != "scope" {
			continue
		}
		if !assigned {
			if i+1 >= len(selectors) {
				return runprogress.Batch{}, fmt.Errorf("--%s requires a value", name)
			}
			i++
			value = selectors[i]
		}
		if name == "scope" && value == "all" {
			value = "installation"
		}
		if name == "run" || name == "launch-id" {
			hasSelection = true
		}
		args = append(args, "--"+name, value)
	}
	if !hasSelection {
		a.mu.Lock()
		id, launchRoot := a.launchID, a.launchRoot
		a.mu.Unlock()
		if id == "" {
			return runprogress.Batch{}, fmt.Errorf("choose /progress --run RUN_ID")
		}
		// Answer/resume can select a run from another project. Follow its new
		// controller receipt while keeping the shell's project selection intact.
		root = launchRoot
		args = append(args, "--launch-id", id)
	}
	if cursor != "" {
		args = append(args, "--cursor", cursor)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, a.executable, args...)
	command.Dir = root
	var output progressBuffer
	var diagnostics boundedBuffer
	command.Stdout, command.Stderr = &output, &diagnostics
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(dashboard.SafeText(diagnostics.String()))
		if message == "" {
			message = err.Error()
		}
		return runprogress.Batch{}, errors.New(message)
	}
	var batch runprogress.Batch
	decoder := json.NewDecoder(strings.NewReader(output.String()))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&batch) != nil || decoder.Decode(new(any)) != io.EOF {
		return batch, fmt.Errorf("invalid CLI progress batch")
	}
	return batch, nil
}

// Progress batches cap event bytes separately from their multi-run cursor.
// Report overflow rather than returning truncated structured output.
type progressBuffer struct{ bytes.Buffer }

func (b *progressBuffer) Write(data []byte) (int, error) {
	if b.Len()+len(data) > 1024*1024 {
		return 0, fmt.Errorf("CLI progress batch exceeds 1 MiB")
	}
	return b.Buffer.Write(data)
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
		return shell.SafeOutput(output.String()), err
	}
	if args[0] == "progress" {
		// JSON shell views are snapshots; continuous output uses Progress.
		args = withoutRunFlags(args, "follow")
		for i := range args {
			if args[i] == "--scope=all" {
				args[i] = "--scope=installation"
			}
			if args[i] == "all" && i > 0 && args[i-1] == "--scope" {
				args[i] = "installation"
			}
		}
		if !containsArg(args, "--once") {
			args = append(args, "--once")
		}
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
	text := shell.SafeOutput(output.String())
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
							text += "\n" + shell.SafeOutput(detail.String())
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
	return shell.SafeOutput(output.String()), err
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
	ticketsCommand := strings.HasPrefix(draft, "/tickets ")
	if position < 0 && !refCommand && !ticketsCommand {
		return nil, nil
	}
	result, err := project.References(ctx, root)
	if err != nil {
		return nil, err
	}
	prefix, needle := draft[:position+1], draft[position+1:]
	if refCommand || ticketsCommand {
		prefix = "/reference "
		if ticketsCommand {
			prefix = "/tickets "
		}
		needle = strings.TrimPrefix(draft, prefix)
		if ticketsCommand && strings.HasPrefix(needle, "--json ") {
			prefix += "--json "
			needle = strings.TrimPrefix(needle, "--json ")
		}
		if ticketsCommand && strings.HasPrefix(needle, "-- ") {
			prefix += "-- "
			needle = strings.TrimPrefix(needle, "-- ")
		}
	}
	needle = strings.Trim(needle, "\"'")
	var suggestions []shell.Suggestion
	for _, ref := range result.References {
		if ref.Error != "" {
			continue
		}
		if refCommand || ticketsCommand {
			if strings.HasPrefix(ref.Name, needle) {
				insertPrefix := prefix
				if ticketsCommand && strings.HasPrefix(ref.Name, "-") && !strings.HasSuffix(prefix, "-- ") {
					insertPrefix += "-- "
				}
				suggestions = append(suggestions, shell.Suggestion{Label: ref.Name, Insert: insertPrefix + strconv.Quote(ref.Name), Description: fmt.Sprintf("%d local tickets", len(ref.Tickets))})
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
